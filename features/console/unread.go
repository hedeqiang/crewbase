package console

// Read position: per conversation, the last event the person has seen
// (any of their devices: it is kept with the session, in the side file
// "read"). The list shows how many replies came after it; opening the
// conversation shows 新消息 above the first of them.

import (
	"strconv"
	"strings"
	"sync"

	"github.com/hedeqiang/crewbase/core/session"
)

const readFile = "read"

var readMu sync.Mutex

// readSeq is the session's read position. A conversation from before
// read positions counts as read up to now (it is written then).
func readSeq(ss *session.Session) int64 {
	readMu.Lock()
	defer readMu.Unlock()
	if b, err := ss.ReadState(readFile); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			return n
		}
	}
	n := int64(len(ss.Events()))
	_ = ss.WriteState(readFile, []byte(strconv.FormatInt(n, 10)))
	return n
}

// markRead moves the read position forward to seq (never back).
func markRead(ss *session.Session, seq int64) int64 {
	cur := readSeq(ss)
	readMu.Lock()
	defer readMu.Unlock()
	if total := int64(len(ss.Events())); seq > total {
		seq = total
	}
	if seq <= cur {
		return cur
	}
	_ = ss.WriteState(readFile, []byte(strconv.FormatInt(seq, 10)))
	return seq
}

// unread counts the replies after the read position: each agent's turn
// that said something, and each approval asked.
func unread(ss *session.Session, from int64) int {
	n := 0
	said := map[string]bool{}
	for _, e := range ss.Since(from) {
		switch e.Type {
		case session.TypeAssistantMessage:
			var m session.AssistantMessage
			if e.Decode(&m) == nil && strings.TrimSpace(m.Message.Text()) != "" && !said[e.Agent] {
				said[e.Agent] = true
				n++
			}
		case session.TypeTurnStart:
			said[e.Agent] = false
		case session.TypeApprovalAsked:
			n++
		}
	}
	return n
}
