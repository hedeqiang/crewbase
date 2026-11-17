package llm

import "testing"

func TestEstimateAttachmentBeforeLoading(t *testing.T) {
	for _, c := range []struct {
		at  Attachment
		min int
		max int
	}{
		{Attachment{Name: "a.png", Mime: "image/png", Size: 900_000}, 1600, 1600},
		{Attachment{Name: "r.txt", Mime: "text/plain", Size: 273_780}, 60_000, 75_000},
		{Attachment{Name: "huge.txt", Mime: "text/plain", Size: 50_000_000}, 120_000, 120_000},
		{Attachment{Name: "d.pdf", Mime: "application/pdf", Size: 400_000}, 11_000, 12_000},
		{Attachment{Name: "loaded.txt", Mime: "text/plain", Size: 273_780, Text: "short"}, 1, 10},
		{Attachment{Name: "x", Mime: "text/plain", Size: 10, Tokens: 777}, 777, 777},
	} {
		if n := EstimateAttachment(&c.at); n < c.min || n > c.max {
			t.Errorf("%s: %d not in [%d, %d]", c.at.Name, n, c.min, c.max)
		}
	}
}
