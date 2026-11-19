package magpie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

// quotaTTL is how long a /v1/magpie/quotas read is reused.
const quotaTTL = 60 * time.Second

// quotaWait bounds how long Quota waits for a fresh read.
const quotaWait = 3 * time.Second

// quotaFailTTL delays the next attempt after a failed read.
const quotaFailTTL = 15 * time.Second

// quotaEntry is one account in magpie's /v1/magpie/quotas.
type quotaEntry struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // subscription | balance | ...
	Plan     string `json:"plan"`
	Windows  []struct {
		Name      string  `json:"name"`
		Used      float64 `json:"used"`      // percent
		Remaining float64 `json:"remaining"` // percent
		ResetsAt  string  `json:"resetsAt"`
		Text      bool    `json:"text"` // display-only (no meaningful percentage)
		Lane      string  `json:"lane"` // e.g. "images": not chat allowance
	} `json:"windows"`
}

// subQuota is one subscription's windows.
type subQuota struct {
	provider string
	label    string
	windows  []llm.QuotaWindow
}

type quotaCache struct {
	subs     []subQuota
	read     time.Time
	ok       bool // the last read succeeded
	retryAt  time.Time
	inflight chan struct{} // closed when the running read finishes
}

// modelProvider is the magpie provider serving a catalog entry
// ("codex/gpt-5" -> "codex"); "" for routing groups.
func modelProvider(m modelEntry) string {
	if m.OwnedBy != "" {
		return m.OwnedBy
	}
	if i := strings.IndexByte(m.ID, '/'); i > 0 {
		return m.ID[:i]
	}
	return ""
}

func (a *Adapter) fetchQuotas(ctx context.Context) ([]subQuota, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.root+"/v1/magpie/quotas", nil)
	req.Header.Set("Authorization", "Bearer "+AppKey)
	req.Header.Set("User-Agent", "Crewbase")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("quotas: %s", resp.Status)
	}
	var body struct {
		Data []quotaEntry `json:"data"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, err
	}
	// several accounts of one provider are told apart by plan, then index
	count := map[string]int{}
	for _, e := range body.Data {
		if e.Kind == "subscription" {
			count[e.Provider]++
		}
	}
	seen := map[string]int{}
	var out []subQuota
	for _, e := range body.Data {
		if e.Kind != "subscription" || e.Provider == "" {
			continue
		}
		label := e.Name
		if label == "" {
			label = e.Provider
		}
		if count[e.Provider] > 1 {
			seen[e.Provider]++
			if e.Plan != "" {
				label += " (" + e.Plan + ")"
			} else {
				label += fmt.Sprintf(" #%d", seen[e.Provider])
			}
		}
		sq := subQuota{provider: e.Provider, label: label}
		for _, w := range e.Windows {
			if w.Text || w.Lane != "" || w.Name == "" {
				continue
			}
			qw := llm.QuotaWindow{Label: label + " · " + w.Name, Remaining: min(max(w.Remaining/100, 0), 1)}
			if t, err := time.Parse(time.RFC3339Nano, w.ResetsAt); err == nil {
				qw.ResetsAt = t
			}
			sq.windows = append(sq.windows, qw)
		}
		if len(sq.windows) > 0 {
			out = append(out, sq)
		}
	}
	return out, nil
}

// subscriptions returns the cached quota read, refreshing it when stale.
// The read runs in the background (magpie may take seconds to ask the
// vendors) and is awaited for at most quotaWait; a slow or failed read
// keeps the previous data (best effort).
func (a *Adapter) subscriptions(ctx context.Context) []subQuota {
	a.quotaMu.Lock()
	now := time.Now()
	q := &a.quota
	if (q.ok && now.Sub(q.read) < quotaTTL) || now.Before(q.retryAt) {
		subs := q.subs
		a.quotaMu.Unlock()
		return subs
	}
	if q.inflight == nil {
		done := make(chan struct{})
		q.inflight = done
		run := func(base context.Context) {
			defer close(done)
			fctx, cancel := context.WithTimeout(base, 20*time.Second)
			defer cancel()
			subs, err := a.fetchQuotas(fctx)
			a.quotaMu.Lock()
			defer a.quotaMu.Unlock()
			q.inflight = nil
			now := time.Now()
			if err != nil {
				q.ok, q.retryAt = false, now.Add(quotaFailTTL)
				if now.Sub(q.read) > 10*quotaTTL {
					q.subs = nil // too old to show
				}
				return
			}
			q.subs, q.read, q.ok, q.retryAt = subs, now, true, time.Time{}
		}
		// bounded by its own timeout, so it never outlives shutdown by much
		go run(context.Background())
	}
	done := q.inflight
	// a stale read is still worth showing: answer with it at once and let
	// the refresh land for the next caller (the model list, which the UI
	// waits on, used to stall here for quotaWait every minute)
	if q.subs != nil {
		subs := q.subs
		a.quotaMu.Unlock()
		return subs
	}
	a.quotaMu.Unlock()
	t := time.NewTimer(quotaWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
	a.quotaMu.Lock()
	defer a.quotaMu.Unlock()
	return q.subs
}

// summarize builds a Quota from subscriptions: every window is listed; the
// headline is the best subscription's tightest window, because magpie can
// serve a request from any of them.
func summarize(subs []subQuota) llm.Quota {
	var q llm.Quota
	best := -1.0
	for _, s := range subs {
		tight := s.windows[0]
		for _, w := range s.windows {
			q.Windows = append(q.Windows, w)
			if w.Remaining < tight.Remaining {
				tight = w
			}
		}
		if tight.Remaining > best {
			best = tight.Remaining
			q.Remaining, q.ResetsAt = tight.Remaining, tight.ResetsAt
		}
	}
	q.Known = len(q.Windows) > 0
	return q
}

// Quota reports the windows of every magpie subscription that serves a
// model in the catalog. Unknown when magpie reports none.
func (a *Adapter) Quota(ctx context.Context) llm.Quota {
	a.mu.RLock()
	provs := a.providers
	a.mu.RUnlock()
	var subs []subQuota
	for _, s := range a.subscriptions(ctx) {
		if provs[s.provider] {
			subs = append(subs, s)
		}
	}
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].provider < subs[j].provider })
	return summarize(subs)
}

// QuotaFor reports the subscriptions behind one model (e.g.
// "codex/gpt-5"); unknown for routing groups and non-subscription models.
func (a *Adapter) QuotaFor(ctx context.Context, model string) llm.Quota {
	a.mu.RLock()
	pv := a.modelProv[model]
	a.mu.RUnlock()
	if pv == "" {
		return llm.Quota{}
	}
	var subs []subQuota
	for _, s := range a.subscriptions(ctx) {
		if s.provider == pv {
			subs = append(subs, s)
		}
	}
	return summarize(subs)
}

var _ llm.QuotaReporter = (*Adapter)(nil)
