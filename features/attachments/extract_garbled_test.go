package attachments

import "testing"

func TestGarbled(t *testing.T) {
	for s, want := range map[string]bool{
		": j\n¥ 860,000\n f\n¥ 1,200,000\n\x0b \x1b Ù æ\n a û Ó ÷ º I \x06   12%   \x0b v  ê \x1a ç j Å ÷ G 9\nPelican 2027   Ù æ À 7": true,
		"海外市场营收增长 12%，主要来自里斯本和东京。":                                                                                                     false,
		"Le café de la gare est à côté de l'hôtel, où l'on mange très bien.":                                                           false,
		"Price: ¥ 1,200,000 (± 5%)": false,
		"":                          false,
	} {
		if got := Garbled(s); got != want {
			t.Errorf("%q: %v", s, got)
		}
	}
}
