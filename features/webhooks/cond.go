package webhooks

// Conditions on an inbound event: a very small expression language over
// the JSON body, the headers and the event type. No functions, no code:
//
//	action == "opened" && pull_request.base.ref == "main"
//	event in ["push", "pull_request"] && !pull_request.draft
//	header.X-GitHub-Event == "check_run" && check_run.conclusion != "success"
//	labels contains "urgent" || title contains "P0"
//
// A bare path is true when it exists and is not false, null, 0 or "".
// Paths are dotted; items of lists are [n]. Roots: event (the verified
// event type), header.<Name> (case-insensitive), anything else reads the
// body (body.x is also accepted).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

// Event is what conditions and templates read.
type Event struct {
	Type    string
	Header  http.Header
	Body    any // decoded JSON (nil when the body is not JSON)
	RawBody []byte
}

// Lookup returns the value at path.
func (e Event) Lookup(path string) (any, bool) {
	path = strings.TrimSpace(path)
	switch {
	case path == "event":
		return e.Type, e.Type != ""
	case strings.HasPrefix(path, "header."):
		v := e.Header.Get(strings.TrimPrefix(path, "header."))
		return v, v != ""
	}
	path = strings.TrimPrefix(path, "body.")
	cur := e.Body
	for _, part := range splitPath(path) {
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[part]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// splitPath: a.b[2].c → a b 2 c
func splitPath(p string) []string {
	p = strings.ReplaceAll(strings.ReplaceAll(p, "[", "."), "]", "")
	var out []string
	for _, x := range strings.Split(p, ".") {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

// ---------------------------------------------------------------- parse

type node interface{ eval(e Event) any }

type (
	orN  struct{ a, b node }
	andN struct{ a, b node }
	notN struct{ x node }
	cmpN struct {
		op   string
		a, b node
	}
	pathN struct{ p string }
	litN  struct{ v any }
	listN struct{ xs []node }
)

func (n orN) eval(e Event) any  { return truthy(n.a.eval(e)) || truthy(n.b.eval(e)) }
func (n andN) eval(e Event) any { return truthy(n.a.eval(e)) && truthy(n.b.eval(e)) }
func (n notN) eval(e Event) any { return !truthy(n.x.eval(e)) }
func (n pathN) eval(e Event) any {
	v, _ := e.Lookup(n.p)
	return v
}
func (n litN) eval(Event) any { return n.v }
func (n listN) eval(e Event) any {
	out := make([]any, len(n.xs))
	for i, x := range n.xs {
		out[i] = x.eval(e)
	}
	return out
}

func (n cmpN) eval(e Event) any {
	a, b := n.a.eval(e), n.b.eval(e)
	switch n.op {
	case "==":
		return equal(a, b)
	case "!=":
		return !equal(a, b)
	case "in":
		if xs, ok := b.([]any); ok {
			for _, x := range xs {
				if equal(a, x) {
					return true
				}
			}
		}
		return false
	case "contains":
		switch x := a.(type) {
		case string:
			return strings.Contains(strings.ToLower(x), strings.ToLower(str(b)))
		case []any:
			for _, y := range x {
				if equal(y, b) {
					return true
				}
				// a list of objects with a name (GitHub labels)
				if m, ok := y.(map[string]any); ok && equal(m["name"], b) {
					return true
				}
			}
		}
		return false
	case ">", "<", ">=", "<=":
		x, ok1 := num(a)
		y, ok2 := num(b)
		if !ok1 || !ok2 {
			return false
		}
		switch n.op {
		case ">":
			return x > y
		case "<":
			return x < y
		case ">=":
			return x >= y
		}
		return x <= y
	}
	return false
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	}
	return true
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func equal(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if x, ok := num(a); ok {
		if y, ok := num(b); ok {
			_, as := a.(string)
			_, bs := b.(string)
			if !(as && bs) {
				return x == y
			}
		}
	}
	return str(a) == str(b)
}

// Cond is a parsed condition ("" = always true).
type Cond struct {
	src  string
	root node
}

// ParseCond parses a condition.
func ParseCond(src string) (Cond, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return Cond{}, nil
	}
	p := &parser{toks: lex(src)}
	n, err := p.or()
	if err != nil {
		return Cond{}, err
	}
	if p.i < len(p.toks) {
		return Cond{}, fmt.Errorf("unexpected %q", p.toks[p.i])
	}
	return Cond{src: src, root: n}, nil
}

// Match reports whether e satisfies the condition.
func (c Cond) Match(e Event) bool { return c.root == nil || truthy(c.root.eval(e)) }

// String is the source.
func (c Cond) String() string { return c.src }

type parser struct {
	toks []string
	i    int
}

func (p *parser) peek() string {
	if p.i < len(p.toks) {
		return p.toks[p.i]
	}
	return ""
}

func (p *parser) next() string { t := p.peek(); p.i++; return t }

func (p *parser) or() (node, error) {
	a, err := p.and()
	for err == nil && p.peek() == "||" {
		p.next()
		var b node
		if b, err = p.and(); err == nil {
			a = orN{a, b}
		}
	}
	return a, err
}

func (p *parser) and() (node, error) {
	a, err := p.unary()
	for err == nil && p.peek() == "&&" {
		p.next()
		var b node
		if b, err = p.unary(); err == nil {
			a = andN{a, b}
		}
	}
	return a, err
}

func (p *parser) unary() (node, error) {
	if p.peek() == "!" {
		p.next()
		x, err := p.unary()
		return notN{x}, err
	}
	if p.peek() == "(" {
		p.next()
		x, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.next() != ")" {
			return nil, fmt.Errorf("missing )")
		}
		return x, nil
	}
	a, err := p.operand()
	if err != nil {
		return nil, err
	}
	switch op := p.peek(); op {
	case "==", "!=", "in", "contains", ">", "<", ">=", "<=":
		p.next()
		b, err := p.operand()
		if err != nil {
			return nil, err
		}
		return cmpN{op: op, a: a, b: b}, nil
	}
	return a, nil
}

func (p *parser) operand() (node, error) {
	t := p.next()
	switch {
	case t == "":
		return nil, fmt.Errorf("unexpected end")
	case t == "[":
		var xs []node
		for p.peek() != "]" {
			x, err := p.operand()
			if err != nil {
				return nil, err
			}
			xs = append(xs, x)
			if p.peek() == "," {
				p.next()
			} else if p.peek() != "]" {
				return nil, fmt.Errorf("expected , or ]")
			}
		}
		p.next()
		return listN{xs}, nil
	case t[0] == '"' || t[0] == '\'':
		return litN{t[1 : len(t)-1]}, nil
	case t == "true", t == "false":
		return litN{t == "true"}, nil
	case t == "null":
		return litN{nil}, nil
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil {
		return litN{f}, nil
	}
	if strings.ContainsAny(t[:1], "=!<>&|(),]") {
		return nil, fmt.Errorf("unexpected %q", t)
	}
	return pathN{t}, nil
}

func lex(s string) []string {
	var out []string
	r := []rune(s)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(r) && r[j] != c {
				j++
			}
			if j >= len(r) {
				out = append(out, string(r[i:])+string(c)) // unterminated: close it
				i = len(r)
				continue
			}
			out = append(out, string(r[i:j+1]))
			i = j + 1
		case strings.ContainsRune("()[],", c):
			out = append(out, string(c))
			i++
		case i+1 < len(r) && strings.Contains("== != >= <= && ||", string(r[i:i+2])) && strings.ContainsRune("=!<>&|", c) && strings.ContainsRune("=&|", r[i+1]):
			out = append(out, string(r[i:i+2]))
			i += 2
		case strings.ContainsRune("!<>", c):
			out = append(out, string(c))
			i++
		default:
			j := i
			for j < len(r) && !unicode.IsSpace(r[j]) && !strings.ContainsRune("()[],!=<>&|\"'", r[j]) {
				j++
				// a list index right after a path is part of it: tags[1].name
				if j > i && j < len(r) && r[j] == '[' {
					k := j + 1
					for k < len(r) && unicode.IsDigit(r[k]) {
						k++
					}
					if k > j+1 && k < len(r) && r[k] == ']' {
						j = k + 1
					}
				}
			}
			// a path may hold a hyphen-free header name (header.X-GitHub-Event)
			if j == i {
				j++
			}
			out = append(out, string(r[i:j]))
			i = j
		}
	}
	return out
}

// ---------------------------------------------------------------- templates

// Render fills {{path}} placeholders from e. {{payload}} is the body
// (pretty JSON, cut at limit), {{event}} the event type.
func Render(tpl string, e Event, limit int) string {
	var b strings.Builder
	for {
		i := strings.Index(tpl, "{{")
		if i < 0 {
			b.WriteString(tpl)
			break
		}
		j := strings.Index(tpl[i:], "}}")
		if j < 0 {
			b.WriteString(tpl)
			break
		}
		b.WriteString(tpl[:i])
		key := strings.TrimSpace(tpl[i+2 : i+j])
		switch key {
		case "payload":
			b.WriteString(Payload(e, limit))
		default:
			if v, ok := e.Lookup(key); ok {
				b.WriteString(clip(str(v), 2000))
			}
		}
		tpl = tpl[i+j+2:]
	}
	return b.String()
}

// Payload is the body for a prompt: pretty JSON when it is JSON, cut.
func Payload(e Event, limit int) string {
	var s string
	if e.Body != nil {
		b, _ := json.MarshalIndent(e.Body, "", "  ")
		s = string(b)
	} else {
		s = string(e.RawBody)
	}
	return clip(s, limit)
}

func clip(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf("\n…[%d more characters]", len(r)-n)
}
