package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"
)

// messageHash is how Claude Code names a request message (api-request
// tail entries, api-request-blob hash): SHA-256 of {"content","role"} as
// compact JSON with sorted keys and unescaped non-ASCII (JSON.stringify
// escaping). Verified against real transcripts: every blob's hash is this
// of its message, and transcript messages match the tail entries that have
// no blob.
func messageHash(role string, content json.RawMessage) (string, error) {
	var c any
	d := json.NewDecoder(bytes.NewReader(content))
	d.UseNumber()
	if err := d.Decode(&c); err != nil {
		return "", err
	}
	var b bytes.Buffer
	canon(&b, map[string]any{"content": c, "role": role})
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func canon(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case json.Number:
		b.WriteString(x.String())
	case string:
		quote(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			canon(b, e)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			quote(b, k)
			b.WriteByte(':')
			canon(b, x[k])
		}
		b.WriteByte('}')
	default:
		fmt.Fprintf(b, "%v", x)
	}
}

// quote writes s as JSON.stringify does: only ", \ and control characters
// are escaped.
func quote(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= utf8.RuneSelf {
			r, n := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && n == 1 {
				b.WriteString(`�`)
			} else {
				b.WriteString(s[i : i+n])
			}
			i += n
			continue
		}
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 {
				fmt.Fprintf(b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
		i++
	}
	b.WriteByte('"')
}
