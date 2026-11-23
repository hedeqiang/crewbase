package common

import (
	"testing"
	"time"

	"github.com/hedeqiang/crewbase/core/llm"
)

func TestClassifySpendLimit(t *testing.T) {
	// Anthropic's spend limit, through magpie (seen 2026-11-23)
	msg := "AlchemyPay-Anthorpic: You have reached your specified API usage limits. You will regain access on 2026-12-01 at 00:00 UTC. (request id: 2026112313411809)"
	e := Classify("magpie", 400, nil, msg, "", "")
	if e.Kind != llm.ErrQuotaExhausted {
		t.Fatalf("kind %s", e.Kind)
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); !e.ResetsAt.Equal(want) {
		t.Fatalf("resets %v", e.ResetsAt)
	}
	if !llm.ShouldFailover(e) {
		t.Fatal("a spent limit does not move to the next model")
	}
	for in, want := range map[string]string{
		"limit resets at 2026-12-03T05:30Z":        "2026-12-03 05:30",
		"you will regain access on 2027-01-01":     "2027-01-01 00:00",
		"nothing about when":                       "",
		"regain access on 2026-13-45 at 99:99 UTC": "",
	} {
		got := ResetIn(in)
		s := ""
		if !got.IsZero() {
			s = got.Format("2006-01-02 15:04")
		}
		if s != want {
			t.Errorf("%q: %q, want %q", in, s, want)
		}
	}
	// a plain bad request stays one
	if e := Classify("openai", 400, nil, "Invalid value: 'tools'", "", ""); e.Kind != llm.ErrBadRequest {
		t.Fatalf("bad request: %s", e.Kind)
	}
}
