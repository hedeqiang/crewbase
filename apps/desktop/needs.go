package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/capabilities/approval"
	"github.com/hedeqiang/crewbase/capabilities/notify"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/kernel"
)

// need is one thing waiting on the person: an approval or a browser
// takeover (docs/03 "需要你").
type need struct {
	Label   string // "Kai 等你批准：git push…"
	Session string // conversation to open
	Asked   time.Time
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func approvalLabel(name, summary string) string {
	if summary == "" {
		return name + " 等你批准"
	}
	return name + " 等你批准：" + clip(summary, 40)
}

func takeoverLabel(name, detail string) string {
	reason := strings.TrimPrefix(detail, "takeover:")
	if reason == "" {
		return name + " 等你接管浏览器"
	}
	return name + " 等你接管浏览器：" + clip(reason, 40)
}

// needs lists what is waiting, oldest first.
func (d *daemon) needs(ctx context.Context) ([]need, error) {
	var out []need
	if d.host != nil {
		if d.closed.Load() {
			return nil, nil
		}
		root := d.host.Root()
		names := map[string]string{}
		agents, hasAgents := kernel.Lookup[*agent.Registry](root, agent.ServiceKey)
		if hasAgents {
			for _, a := range agents.List() {
				names[a.Spec().ID] = a.Spec().Name
				if st, detail := a.Status(); st == agent.StatusWaiting && strings.HasPrefix(detail, "takeover:") {
					out = append(out, need{Label: takeoverLabel(a.Spec().Name, detail), Session: a.StatusSession()})
				}
			}
		}
		if in, ok := kernel.Lookup[*approval.Inbox](root, approval.InboxKey); ok {
			for _, r := range in.Pending() {
				out = append(out, need{Label: approvalLabel(nameOr(names, r.Agent), r.Summary), Session: r.Session, Asked: r.Asked})
			}
		}
	} else {
		var ag struct {
			Agents []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				Status        string `json:"status"`
				Detail        string `json:"detail"`
				StatusSession string `json:"statusSession"`
			} `json:"agents"`
		}
		var ap struct {
			Pending []approval.Request `json:"pending"`
		}
		if err := d.getJSON(ctx, "/api/agents", &ag); err != nil {
			return nil, err
		}
		if err := d.getJSON(ctx, "/api/approvals", &ap); err != nil {
			return nil, err
		}
		names := map[string]string{}
		for _, a := range ag.Agents {
			names[a.ID] = a.Name
			if a.Status == string(agent.StatusWaiting) && strings.HasPrefix(a.Detail, "takeover:") {
				out = append(out, need{Label: takeoverLabel(a.Name, a.Detail), Session: a.StatusSession})
			}
		}
		for _, r := range ap.Pending {
			out = append(out, need{Label: approvalLabel(nameOr(names, r.Agent), r.Summary), Session: r.Session, Asked: r.Asked})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Asked.Before(out[j].Asked) })
	return out, nil
}

func nameOr(names map[string]string, id string) string {
	if n := names[id]; n != "" {
		return n
	}
	return id
}

func (d *daemon) getJSON(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL()+path, nil)
	req.Header.Set("Authorization", "Bearer "+d.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// watchEvents follows the attached daemon's SSE stream (/api/events):
// approvals and agent status changes refresh the "needs you" set, and
// "notify" events become native notifications. It reconnects with backoff
// for as long as the shell runs (the daemon may restart, e.g. on update).
func (d *daemon) watchEvents() {
	backoff := time.Second
	for {
		err := d.streamOnce()
		slog.Info("desktop: event stream ended; reconnecting", "err", err, "in", backoff)
		d.poke() // resync after the gap
		time.Sleep(backoff)
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (d *daemon) streamOnce() error {
	req, _ := http.NewRequest(http.MethodGet, d.baseURL()+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+d.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events: %s", resp.Status)
	}
	d.poke()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var event string
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			d.onEvent(event, data.String())
			event = ""
			data.Reset()
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream closed")
}

func (d *daemon) onEvent(event, data string) {
	switch event {
	case "approvals.changed", "agent.status", "agent.deleted":
		d.poke()
	case "notify":
		var n notify.Notification
		if json.Unmarshal([]byte(data), &n) == nil && n.Title != "" {
			d.post(n)
		}
	}
}
