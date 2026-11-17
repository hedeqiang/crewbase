package acp

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hedeqiang/crewbase/bridge/jsonrpc"
	"github.com/hedeqiang/crewbase/core/llm"
)

// Text signals emitted by the CLIs (or their ACP adapters) for failures the
// router cares about. Matched case-insensitively against JSON-RPC error
// messages/data and the tail of the process's stderr.
var (
	reQuota = regexp.MustCompile(`(?i)usage limit|quota|5-hour limit|five-hour limit|weekly limit|daily limit|session limit|limit reached|hit your limit|resource_exhausted|credit balance|out of credits|purchase more credits|upgrade to (plus|pro)`)
	reRate  = regexp.MustCompile(`(?i)rate.?limit|too many requests|\b429\b`)
	reAuth  = regexp.MustCompile(`(?i)authentication required|auth required|run /login|not logged in|please log ?in|login required|unauthori[sz]ed|\b401\b|invalid api key|refresh token|token (has )?expired|codex login`)
	reCtx   = regexp.MustCompile(`(?i)context window|context length|context_length_exceeded|prompt is too long|ran out of room`)
	// A model the account or plan cannot use ("The 'gpt-6.1-sol' model is
	// not supported when using Codex with a ChatGPT account").
	rePlanModel = regexp.MustCompile(`(?i)model\b.*\b(not supported when using|is not supported (on|for|with)|not available (on|for|with|to)|not enabled for|requires a)\b.*\b(account|plan|subscription|tier)\b`)
	reBusy      = regexp.MustCompile(`(?i)overloaded|high demand|service unavailable|\b50[23]\b`)

	reRetryIn = regexp.MustCompile(`(?i)(?:try again|retry|resets?) in (\d+)\s*(second|sec|minute|min|hour|hr|day)s?`)
	reEpoch   = regexp.MustCompile(`\|(\d{10})\b`) // "Claude AI usage limit reached|1735689600"
	// A short assistant reply that is only a limit banner.
	reLimitBanner = regexp.MustCompile(`(?i)^\W*(claude ai usage limit reached|you've hit your (usage )?limit|(5-hour|weekly|daily|usage) limit reached)`)
)

// classifyText maps free text to an error kind ("" if nothing matched).
func classifyText(s string) llm.ErrorKind {
	switch {
	case s == "":
		return ""
	case rePlanModel.MatchString(s):
		return llm.ErrModelUnavailable
	case reQuota.MatchString(s):
		return llm.ErrQuotaExhausted
	case reRate.MatchString(s):
		return llm.ErrRateLimited
	case reAuth.MatchString(s):
		return llm.ErrAuth
	case reCtx.MatchString(s):
		return llm.ErrContextOverflow
	case reBusy.MatchString(s):
		return llm.ErrUnavailable
	}
	return ""
}

// resetHints extracts retry/reset hints from text.
func resetHints(s string, now time.Time) (retry time.Duration, resets time.Time) {
	if m := reEpoch.FindStringSubmatch(s); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			resets = time.Unix(sec, 0)
		}
	}
	if m := reRetryIn.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := time.Second
		switch strings.ToLower(m[2]) {
		case "minute", "min":
			unit = time.Minute
		case "hour", "hr":
			unit = time.Hour
		case "day":
			unit = 24 * time.Hour
		}
		retry = time.Duration(n) * unit
		if resets.IsZero() {
			resets = now.Add(retry)
		}
	}
	if retry == 0 && !resets.IsZero() && resets.After(now) {
		retry = resets.Sub(now)
	}
	return retry, resets
}

// classify turns a failure (JSON-RPC error, transport error, ...) plus the
// process's recent stderr into a *llm.Error. fallback is the kind used when
// no signal matched ("" leaves the error unclassified as ErrUnavailable is
// not always right, e.g. bad params).
func classify(provider string, err error, stderr string, fallback llm.ErrorKind) error {
	if err == nil {
		return nil
	}
	var le *llm.Error
	if errors.As(err, &le) {
		return err
	}
	msg := err.Error()
	kind := llm.ErrorKind("")
	var je *jsonrpc.Error
	if errors.As(err, &je) {
		text := je.Message + " " + string(je.Data)
		kind = classifyText(text)
		if kind == "" && je.Code == codeAuthRequired {
			kind = llm.ErrAuth
		}
		if kind == "" && (je.Code == jsonrpc.CodeInvalidParams || je.Code == jsonrpc.CodeInvalidRequest) {
			kind = llm.ErrBadRequest
		}
	}
	if kind == "" {
		kind = classifyText(msg)
	}
	if kind == "" && stderr != "" {
		if k := classifyText(stderr); k != "" {
			kind = k
			msg += " (stderr: " + lastLine(stderr) + ")"
		}
	}
	if kind == "" {
		kind = fallback
	}
	if kind == "" {
		return err
	}
	retry, resets := resetHints(msg+"\n"+stderr, time.Now())
	return &llm.Error{Kind: kind, Provider: provider, Message: msg, RetryAfter: retry, ResetsAt: resets}
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 300 {
		s = s[len(s)-300:]
	}
	return s
}

// ringBuffer keeps the last max bytes written (stderr tail).
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newRing(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.max:]...)
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
