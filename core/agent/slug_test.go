package agent

import "testing"

func TestSlugReadableForChinese(t *testing.T) {
	for in, want := range map[string]string{
		"调研员":           "diao-yan-yuan",
		"My Agent":      "my-agent",
		"前端 Dev 2号":     "qian-duan-dev-2-hao",
		"🙂":             "agent",
		"":              "agent",
		"Code Reviewer": "code-reviewer",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
