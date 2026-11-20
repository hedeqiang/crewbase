package console

// Search across conversations (⌘K): the person's and agents' messages in
// every conversation the caller may see, newest first, each with a
// snippet around the match.

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/session"
)

const maxSearchHits = 30

// mdPlain drops Markdown marks from a snippet (**bold**, `code`, # heads).
var mdPlain = strings.NewReplacer("**", "", "__", "", "`", "", "### ", "", "## ", "", "# ", "", "> ", "")

// SearchHit is one message found.
type SearchHit struct {
	Session string    `json:"session"`
	Seq     int64     `json:"seq"`
	Agent   string    `json:"agent,omitempty"` // who wrote it ("" = a person)
	From    string    `json:"from,omitempty"`  // a team member's name
	Snippet string    `json:"snippet"`
	Time    time.Time `json:"time"`
}

// snippetAround is about n runes of text around the first match of ql.
func snippetAround(text, ql string, n int) string {
	text = mdPlain.Replace(text)
	text = strings.Join(strings.Fields(text), " ")
	low := strings.ToLower(text)
	i := strings.Index(low, ql)
	if i < 0 {
		return ""
	}
	start := utf8.RuneCountInString(low[:i])
	r := []rune(text)
	from := max(0, start-n/3)
	to := min(len(r), from+n)
	out := string(r[from:to])
	if from > 0 {
		out = "…" + out
	}
	if to < len(r) {
		out += "…"
	}
	return out
}

// searchSessions finds q in the messages of sessions (visible decides).
func searchSessions(list []*session.Session, q string, visible func(id string) bool) []SearchHit {
	ql := strings.ToLower(strings.TrimSpace(q))
	hits := []SearchHit{}
	if utf8.RuneCountInString(ql) < 2 {
		return hits
	}
	for _, ss := range list {
		if !visible(ss.ID()) {
			continue
		}
		for _, e := range ss.Events() {
			var text, from string
			switch e.Type {
			case session.TypeUserMessage:
				var m session.UserMessage
				if e.Decode(&m) != nil {
					continue
				}
				text, from = m.Text, m.From
			case session.TypeAssistantMessage:
				var m session.AssistantMessage
				if e.Decode(&m) != nil {
					continue
				}
				text = m.Message.Text()
			default:
				continue
			}
			if text == "" || !strings.Contains(strings.ToLower(mdPlain.Replace(text)), ql) {
				continue
			}
			hits = append(hits, SearchHit{Session: ss.ID(), Seq: e.Seq, Agent: e.Agent, From: from, Snippet: snippetAround(text, ql, 90), Time: e.Time})
		}
	}
	// newest first, a few per conversation at most
	sortHits(hits)
	per := map[string]int{}
	out := hits[:0]
	for _, h := range hits {
		if per[h.Session] >= 5 || len(out) >= maxSearchHits {
			continue
		}
		per[h.Session]++
		out = append(out, h)
	}
	return out
}

func sortHits(h []SearchHit) {
	for i := 1; i < len(h); i++ {
		for j := i; j > 0 && h[j].Time.After(h[j-1].Time); j-- {
			h[j], h[j-1] = h[j-1], h[j]
		}
	}
}
