package autodraft

// 做成技能: a skill grown from use. The same transcript a routine is drafted
// from (what the person asked, what the agent did, which tools it called
// and what they returned) is turned into a SKILL.md: how to do this kind
// of task again, for any agent. Nothing is written here; the person edits
// the draft in the conversation and saves it through POST /api/skills.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hedeqiang/crewbase/core/llm"
	"github.com/hedeqiang/crewbase/core/session"
)

// SkillDraft is a proposed skill: its folder name, what it is for, and
// the whole SKILL.md (front matter and body), ready to save.
type SkillDraft struct {
	Name        string `json:"name"`        // folder name: lowercase, digits, dashes
	Description string `json:"description"` // when to use it (the catalog line agents see)
	Content     string `json:"content"`     // the SKILL.md
	Model       string `json:"model,omitempty"`
	From        int64  `json:"from"`
	Upto        int64  `json:"upto"`
}

const skillPrompt = `You turn a conversation between a person and an AI agent into a reusable skill: written instructions any agent can follow to do this kind of task again, without the conversation.
Read the transcript. Find what generalises: the kind of task, the steps that worked, the tools and commands used (and with which options), the checks that showed it was done, the mistakes made and corrected along the way. Leave out what was only true this once (one file's name, one person's data) unless it is the point; say how to find it instead.

Answer with ONE JSON object and nothing else (no code fences):
{
  "name": "folder name in English: lowercase letters, digits and dashes, 3 to 40 characters, e.g. release-notes or ci-failure-triage",
  "description": "one sentence in the conversation's language: what the skill does and when to use it (agents choose skills by this line)",
  "body": "the instructions in Markdown, in the conversation's language: a short purpose, '## 步骤' / '## Steps' as a numbered list with the exact commands and tools, '## 检查' / '## Checks' for how to tell it worked, and '## 注意' / '## Pitfalls' for what went wrong and how it was fixed. No front matter."
}`

type skillAnswer struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// skillName makes s a folder name the skills service takes ("" when nothing is left).
func skillName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if !skillNameRe.MatchString(out) {
		return ""
	}
	return out
}

// skillFile writes the SKILL.md: front matter (name, description) and body.
func skillFile(name, desc, body string) string {
	q, _ := json.Marshal(strings.Join(strings.Fields(desc), " ")) // a JSON string is valid YAML
	return "---\nname: " + name + "\ndescription: " + string(q) + "\n---\n\n" + strings.TrimSpace(body) + "\n"
}

func parseSkill(text string) (skillAnswer, error) {
	var a skillAnswer
	// an outer ```json fence: only it is dropped (the body holds fences of
	// its own, so a regex to the next ``` would cut it short)
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if nl := strings.IndexByte(text, '\n'); nl > 0 {
			text = strings.TrimSuffix(strings.TrimSpace(text[nl+1:]), "```")
		}
	}
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i {
		return a, errors.New("no JSON object in the answer")
	}
	if err := json.Unmarshal([]byte(text[i:j+1]), &a); err != nil {
		return a, err
	}
	if strings.TrimSpace(a.Body) == "" || strings.TrimSpace(a.Description) == "" {
		return a, errors.New("the answer has no instructions")
	}
	return a, nil
}

// MakeSkill drafts a skill from session ss (the same stretch of
// conversation a routine draft reads).
func (s *Service) MakeSkill(ctx context.Context, ss *session.Session, req Request) (SkillDraft, error) {
	agentID := req.Agent
	if agentID == "" {
		if ags := ss.Meta().Agents; len(ags) > 0 {
			agentID = ags[0]
		}
	}
	a, ok := s.agents.Get(agentID)
	if !ok {
		return SkillDraft{}, fmt.Errorf("unknown agent %q", agentID)
	}
	text, from, upto := transcript(ss.Events(), a.ID(), req.Upto, req.Scope)
	if strings.TrimSpace(text) == "" {
		return SkillDraft{}, errors.New("nothing in this stretch of conversation to draft from")
	}
	var last error
	for _, ref := range s.candidates(a) {
		ad, found := s.llms.Get(ref.Provider)
		if !found {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		resp, err := ad.Stream(tctx, llm.Request{Model: ref.Model, Effort: "minimal", MaxTokens: 3000, System: skillPrompt,
			Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "Transcript:\n\n"+text)}, Agent: a.ID(), Session: ss.ID()}, func(llm.Chunk) error { return nil })
		cancel()
		if err != nil {
			last = err
			s.c.Log().Info("skill draft: model failed, trying the next", "model", ref.String(), "err", err)
			continue
		}
		ans, err := parseSkill(resp.Message.Text())
		if err != nil {
			last = err
			s.c.Log().Info("skill draft: unusable answer, trying the next", "model", ref.String(), "err", err)
			continue
		}
		name := skillName(ans.Name)
		if name == "" {
			name = "skill-" + time.Now().Format("0102-1504")
		}
		desc := strings.TrimSpace(ans.Description)
		if utf8.RuneCountInString(desc) > 300 {
			desc = string([]rune(desc)[:300])
		}
		return SkillDraft{Name: name, Description: desc, Content: skillFile(name, desc, ans.Body), Model: ref.String(), From: from, Upto: upto}, nil
	}
	if last == nil {
		last = errors.New("no model available to draft with")
	}
	return SkillDraft{}, last
}
