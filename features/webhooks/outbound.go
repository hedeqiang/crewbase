package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/vcs"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/core/agentloop"
	"github.com/hedeqiang/crewbase/features/channel"
	"github.com/hedeqiang/crewbase/features/routines"
	"github.com/hedeqiang/crewbase/features/webhooks/verifiers"
	"github.com/hedeqiang/crewbase/features/workflows"
	"github.com/hedeqiang/crewbase/internal/netguard"
	"github.com/hedeqiang/crewbase/kernel"
)

// Outbound event names.
const (
	OutWorkflowFinished = "workflow.finished"
	OutRoutineFinished  = "routine.finished"
	OutApprovalAsked    = "approval.asked"
	OutApprovalDecided  = "approval.decided"
	OutTurnEnded        = "agent.turn_ended"
	OutOwnerChanged     = "channel.owner_changed"
	OutGitMerged        = "git.merged"
	OutGitReview        = "git.review"
	OutEmit             = "agent.emit" // webhook_emit
	OutTest             = "webhook.test"
)

// OutEvents lists the outbound events (the settings page offers them).
func OutEvents() []string {
	return []string{OutWorkflowFinished, OutRoutineFinished, OutApprovalAsked, OutApprovalDecided, OutTurnEnded, OutOwnerChanged, OutGitMerged, OutGitReview, OutEmit}
}

// Envelope is the JSON body of an outbound delivery.
type Envelope struct {
	ID    string    `json:"id"`
	Event string    `json:"event"`
	Time  time.Time `json:"time"`
	Team  string    `json:"team"`
	Data  any       `json:"data"`
}

// ---------------------------------------------------------------- subscriptions

// SubscriptionInput creates or changes a subscription.
type SubscriptionInput struct {
	Name    *string   `json:"name"`
	URL     *string   `json:"url"`
	Events  *[]string `json:"events"`
	Filter  *string   `json:"filter"`
	Secret  *string   `json:"secret"`
	Enabled *bool     `json:"enabled"`
}

// Subscriptions of a team, newest first.
func (s *Service) Subscriptions(team string) []Subscription {
	team = agent.TeamOf(team)
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	out := []Subscription{}
	for i := len(s.st.st.Subscriptions) - 1; i >= 0; i-- {
		if x := s.st.st.Subscriptions[i]; x.Team == team {
			out = append(out, *x)
		}
	}
	return out
}

// Subscription returns one of team.
func (s *Service) Subscription(team, id string) (Subscription, bool) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	for _, x := range s.st.st.Subscriptions {
		if x.ID == id && x.Team == agent.TeamOf(team) {
			return *x, true
		}
	}
	return Subscription{}, false
}

// SaveSubscription creates or changes a subscription; a generated signing
// secret comes back once.
func (s *Service) SaveSubscription(team, id, by string, in SubscriptionInput) (Subscription, string, error) {
	team = agent.TeamOf(team)
	var x Subscription
	if id != "" {
		cur, ok := s.Subscription(team, id)
		if !ok {
			return Subscription{}, "", ErrNotFound
		}
		x = cur
	} else {
		x = Subscription{ID: newID("sb"), Team: team, Enabled: true, By: by, Created: s.now().UTC()}
	}
	if in.Name != nil {
		x.Name = strings.TrimSpace(*in.Name)
	}
	if in.URL != nil {
		x.URL = strings.TrimSpace(*in.URL)
	}
	if in.Events != nil {
		x.Events = cleanList(*in.Events)
	}
	if in.Filter != nil {
		x.Filter = strings.TrimSpace(*in.Filter)
	}
	if in.Enabled != nil {
		x.Enabled = *in.Enabled
	}
	u, err := validURL(x.URL)
	if err != nil {
		return Subscription{}, "", err
	}
	if x.Name == "" {
		x.Name = u.Host
	}
	if len(x.Events) == 0 {
		return Subscription{}, "", bad("pick at least one event")
	}
	for _, e := range x.Events {
		if e != "*" && !slices.Contains(OutEvents(), e) {
			return Subscription{}, "", bad("unknown event %q", e)
		}
	}
	if _, err := ParseCond(x.Filter); err != nil {
		return Subscription{}, "", bad("condition: %v", err)
	}
	shown := ""
	ref := "vault:webhooks/" + x.ID
	if in.Secret != nil && strings.TrimSpace(*in.Secret) != "" {
		if err := s.setSecret(ref, strings.TrimSpace(*in.Secret)); err != nil {
			return Subscription{}, "", err
		}
		x.Secret = ref
	} else if x.Secret == "" {
		shown = randToken()
		if err := s.setSecret(ref, shown); err != nil {
			return Subscription{}, "", err
		}
		x.Secret = ref
	}
	x.Updated = s.now().UTC()
	s.st.mu.Lock()
	found := false
	for i, y := range s.st.st.Subscriptions {
		if y.ID == x.ID {
			s.st.st.Subscriptions[i] = &x
			found = true
		}
	}
	if !found {
		s.st.st.Subscriptions = append(s.st.st.Subscriptions, &x)
	}
	err = s.st.saveState()
	s.st.mu.Unlock()
	if err != nil {
		return Subscription{}, "", err
	}
	s.record("webhook/subscription.saved", by, map[string]any{"id": x.ID, "team": team, "host": u.Host, "events": x.Events})
	s.changed(team, "subscription", x.ID)
	return x, shown, nil
}

// DeleteSubscription removes a subscription (its pending deliveries stop).
func (s *Service) DeleteSubscription(team, id, by string) error {
	x, ok := s.Subscription(team, id)
	if !ok {
		return ErrNotFound
	}
	s.st.mu.Lock()
	s.st.st.Subscriptions = slices.DeleteFunc(s.st.st.Subscriptions, func(y *Subscription) bool { return y.ID == id })
	for _, d := range s.st.dels {
		if d.Sub == id && d.Status == StPending {
			d.Status, d.Error = StDead, "the subscription was deleted"
		}
	}
	err := s.st.saveState()
	_ = s.st.saveDeliveries()
	s.st.mu.Unlock()
	if cs := s.creds(); cs != nil && strings.HasPrefix(x.Secret, "vault:webhooks/") {
		_ = cs.Delete(x.Secret)
	}
	s.record("webhook/subscription.deleted", by, map[string]any{"id": id})
	s.changed(x.Team, "subscription", id)
	return err
}

// ---------------------------------------------------------------- publishing

// Publish queues event for every subscription of team that wants it.
// data is what the receiver gets in the envelope's data.
func (s *Service) Publish(team, event string, data any) int {
	team = agent.TeamOf(team)
	s.st.mu.Lock()
	subs := []Subscription{}
	for _, x := range s.st.st.Subscriptions {
		if x.Team == team && x.Enabled && (slices.Contains(x.Events, event) || slices.Contains(x.Events, "*") && event != OutTest) {
			subs = append(subs, *x)
		}
	}
	s.st.mu.Unlock()
	n := 0
	for _, x := range subs {
		if x.Filter != "" {
			c, err := ParseCond(x.Filter)
			if err != nil || !c.Match(Event{Type: event, Body: asJSON(data)}) {
				continue
			}
		}
		s.enqueue(x, event, data, "")
		n++
	}
	return n
}

// asJSON turns data into what conditions read (maps and lists).
func asJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Service) enqueue(x Subscription, event string, data any, via string) Delivery {
	now := s.now().UTC()
	id := newID("dl")
	env := Envelope{ID: id, Event: event, Time: now, Team: x.Team, Data: data}
	body, _ := json.Marshal(env)
	d := &Delivery{ID: id, Dir: "out", Team: x.Team, Sub: x.ID, Name: x.Name, Event: event, Key: id, Via: via, Status: StPending, Next: now, At: now, Body: body}
	s.save(d)
	select {
	case s.kick <- struct{}{}:
	default:
	}
	return *d
}

// SendTest queues a test event to one subscription.
func (s *Service) SendTest(team, id, by string) (Delivery, error) {
	x, ok := s.Subscription(team, id)
	if !ok {
		return Delivery{}, ErrNotFound
	}
	return s.enqueue(x, OutTest, map[string]any{"message": "A test delivery from Crewbase.", "by": by}, "test"), nil
}

func (s *Service) requeue(d Delivery, by string) (Delivery, error) {
	if d.Status != StDead {
		return Delivery{}, bad("only a dead delivery can be sent again (this one is %s)", d.Status)
	}
	if _, ok := s.Subscription(d.Team, d.Sub); !ok {
		return Delivery{}, bad("its subscription is gone")
	}
	d.Status, d.Attempts, d.Next, d.Error, d.Code = StPending, 0, s.now().UTC(), "", 0
	s.save(&d)
	s.record("webhook/replayed", by, map[string]any{"delivery": d.ID, "subscription": d.Sub})
	select {
	case s.kick <- struct{}{}:
	default:
	}
	return d, nil
}

// ---------------------------------------------------------------- worker

// outbox holds the HTTP client built from the settings (private ranges).
type outbox struct {
	mu     sync.Mutex
	client *http.Client
	allow  func() []netip.Prefix
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (o *outbox) reset() {
	o.mu.Lock()
	o.client = nil
	o.mu.Unlock()
}

func (o *outbox) get() *http.Client {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.client == nil {
		g := &netguard.Guard{Allow: o.allow(), Lookup: o.lookup}
		o.client = g.Client(15*time.Second, 3)
	}
	return o.client
}

func parsePrefixes(xs []string) ([]netip.Prefix, error) { return netguard.ParsePrefixes(xs) }

// Backoff is the wait before attempt n+1 (n ≥ 1): 30s, 1m, 2m … capped at 1h.
func Backoff(n int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < n && d < time.Hour; i++ {
		d *= 2
	}
	return min(d, time.Hour)
}

func (s *Service) worker(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		s.deliverDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// deliverDue sends every pending delivery whose time has come.
func (s *Service) deliverDue(ctx context.Context) {
	now := s.now()
	s.st.mu.Lock()
	var due []*Delivery
	for _, d := range s.st.dels {
		if d.Dir == "out" && d.Status == StPending && !d.Next.After(now) {
			due = append(due, d)
		}
	}
	s.st.mu.Unlock()
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		s.attempt(ctx, *d)
	}
}

func (s *Service) attempt(ctx context.Context, d Delivery) {
	x, ok := s.Subscription(d.Team, d.Sub)
	if !ok || !x.Enabled {
		d.Status, d.Error = StDead, "the subscription is gone or switched off"
		s.save(&d)
		return
	}
	d.Attempts++
	code, err := s.post(ctx, x, d)
	d.Code = code
	switch {
	case err == nil && code >= 200 && code < 300:
		d.Status, d.Error, d.Next = StDelivered, "", time.Time{}
	default:
		if err != nil {
			d.Error = err.Error()
		} else {
			d.Error = fmt.Sprintf("the receiver answered %d", code)
		}
		max := s.st.settings().MaxAttempts
		// a 4xx other than 408/429 will not get better by itself
		final := code >= 400 && code < 500 && code != 408 && code != 429
		if d.Attempts >= max || final || strings.Contains(d.Error, netguard.ErrBlocked.Error()) {
			d.Status = StDead
		} else {
			d.Next = s.now().UTC().Add(Backoff(d.Attempts))
		}
	}
	s.save(&d)
	if d.Status != StPending {
		s.record("webhook/delivered", "webhooks", map[string]any{"delivery": d.ID, "subscription": d.Sub, "event": d.Event, "status": d.Status, "code": d.Code, "attempts": d.Attempts, "error": d.Error})
	}
}

func (s *Service) post(ctx context.Context, x Subscription, d Delivery) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.URL, bytes.NewReader(d.Body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Crewbase-Webhooks/1")
	req.Header.Set("X-Crewbase-Event", d.Event)
	req.Header.Set("X-Crewbase-Delivery", d.ID)
	if x.Secret != "" {
		sec, err := s.secret(ctx, x.Secret)
		if err != nil {
			return 0, fmt.Errorf("signing secret: %w", err)
		}
		req.Header.Set("X-Crewbase-Signature-256", verifiers.Sign(sec, s.now(), d.Body))
	}
	resp, err := s.out.get().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// ---------------------------------------------------------------- bridges

// bridge turns kernel events into outbound events.
func (s *Service) bridge(c *kernel.Context) {
	teamOf := func(agentID string) string {
		if a, ok := s.agents.Get(agentID); ok {
			return a.Spec().TeamID()
		}
		return ""
	}
	workflows.RunFinished.Listen(c, func(r workflows.RunDone) {
		team := ""
		if len(r.Agents) > 0 {
			team = teamOf(r.Agents[0])
		}
		if team != "" {
			s.Publish(team, OutWorkflowFinished, map[string]any{"workflow": r.Workflow, "name": r.Name, "run": r.Run, "status": r.Status,
				"trigger": r.Trigger, "output": clip(r.Output, 4000), "error": r.Error})
		}
	})
	routines.RunFinished.Listen(c, func(r routines.RunDone) {
		if team := teamOf(r.Agent); team != "" {
			s.Publish(team, OutRoutineFinished, map[string]any{"routine": r.Routine, "name": r.Name, "agent": r.Agent, "status": r.Status,
				"summary": r.Summary, "session": r.Session})
		}
	})
	var mu sync.Mutex
	asked := map[string]approval.Request{} // callId → request (decided carries no agent)
	approval.Asked.Listen(c, func(r approval.Request) {
		mu.Lock()
		asked[r.CallID] = r
		mu.Unlock()
		if team := teamOf(r.Agent); team != "" {
			s.Publish(team, OutApprovalAsked, map[string]any{"callId": r.CallID, "agent": r.Agent, "session": r.Session, "tool": r.Tool,
				"category": r.Category, "summary": r.Summary})
		}
	})
	approval.Decided.Listen(c, func(d approval.DecidedData) {
		mu.Lock()
		r, ok := asked[d.CallID]
		delete(asked, d.CallID)
		mu.Unlock()
		if !ok {
			return
		}
		if team := teamOf(r.Agent); team != "" {
			s.Publish(team, OutApprovalDecided, map[string]any{"callId": d.CallID, "agent": r.Agent, "session": r.Session, "tool": r.Tool,
				"outcome": d.Outcome, "summary": r.Summary})
		}
	})
	agentloop.TurnEnded.Listen(c, func(e agentloop.TurnEndEvent) {
		if a, ok := s.agents.Get(e.Agent); ok && !a.Spec().Ephemeral {
			s.Publish(a.Spec().TeamID(), OutTurnEnded, map[string]any{"agent": e.Agent, "session": e.Session, "status": e.Status,
				"error": e.Error, "steps": e.Steps, "from": e.Input.From, "text": clip(e.Text, 2000)})
		}
	})
	channel.OwnerChanged.Listen(c, func(e channel.OwnerEvent) {
		if team := teamOf(e.Owner); team != "" {
			s.Publish(team, OutOwnerChanged, map[string]any{"channel": e.Channel, "owner": e.Owner, "prev": e.Prev, "by": e.By, "reason": e.Reason})
		}
	})
	vcs.Changed.Listen(c, func(e vcs.Event) {
		var name string
		switch {
		case e.Type == "git/merged":
			name = OutGitMerged
		case strings.HasPrefix(e.Type, "git/review."):
			name = OutGitReview
		default:
			return
		}
		ag := e.Agent
		if ag == "" {
			ag, _ = strings.CutPrefix(e.Actor, "agent:")
		}
		team := teamOf(ag)
		if team == "" {
			team = agent.DefaultTeam
		}
		data := map[string]any{"type": e.Type, "repo": e.Repo, "id": e.ID, "actor": e.Actor}
		if m, ok := e.Data.(map[string]any); ok {
			for k, v := range m {
				data[k] = v
			}
		}
		s.Publish(team, name, data)
	})
}
