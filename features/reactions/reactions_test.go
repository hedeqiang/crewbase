package reactions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hedeqiang/crewbase/core/session"
	"github.com/hedeqiang/crewbase/core/tools"
)

func TestValid(t *testing.T) {
	for _, ok := range []string{"👍", "❤️", "👨‍👩‍👧", "✅", "🎉"} {
		if !valid(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", "ok", "👍 ", "<b>", "1", "好", "👍👍👍👍👍👍👍👍👍"} {
		if valid(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestReactTool(t *testing.T) {
	st, _ := session.NewStore(nil, "")
	ss, _ := st.Create(session.Meta{Kind: "channel"})
	first, _ := ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: "for kai", To: "kai"})
	ss.Append(session.TypeUserMessage, "", session.UserMessage{Text: "for mia", To: "mia"})
	args, _ := json.Marshal(map[string]string{"emoji": "👀"})
	res, err := tool().Execute(context.Background(), tools.Call{Agent: "kai", Session: ss, Args: args})
	if err != nil || res.IsError {
		t.Fatalf("%+v %v", res, err)
	}
	e, _ := ss.Last(session.TypeReaction)
	var d Data
	_ = e.Decode(&d)
	if d.Target != first.Seq || d.Emoji != "👀" || e.Agent != "kai" {
		t.Fatalf("%+v %s", d, e.Agent)
	}
	args, _ = json.Marshal(map[string]string{"emoji": "great"})
	if res, _ := tool().Execute(context.Background(), tools.Call{Agent: "kai", Session: ss, Args: args}); !res.IsError {
		t.Fatal("text accepted")
	}
}
