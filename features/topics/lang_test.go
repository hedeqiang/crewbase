package topics

import (
	"strings"
	"testing"
)

func TestSystemForNamesTheLanguage(t *testing.T) {
	for in, want := range map[string]string{
		"We ship the new onboarding on Friday": "write the topic in English",
		"帮我拆一下周五的发布任务":                         "Write it in Chinese",
		"Fix the 登录 bug":                       "Write it in Chinese",
		"Résumé du projet":                     "",
		"12345":                                "",
	} {
		got := systemFor(in)
		if want == "" {
			if got != prompt {
				t.Errorf("%q: %q, want the plain prompt", in, got)
			}
		} else if !strings.Contains(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
