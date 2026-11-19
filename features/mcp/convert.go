package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/core/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MaxToolName is the longest tool name the model APIs accept.
const MaxToolName = 64

// Separator joins the server namespace and the MCP tool name.
const Separator = "__"

var unsafeToolChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ToolName returns the namespaced, model-safe name "<server>__<tool>":
// characters outside [a-zA-Z0-9_-] become "_", and names longer than
// MaxToolName are cut and suffixed with a short hash of the original so two
// long names stay distinct.
func ToolName(server, tool string) string {
	n := server + Separator + unsafeToolChars.ReplaceAllString(tool, "_")
	if len(n) <= MaxToolName {
		return n
	}
	h := fnv.New32a()
	h.Write([]byte(tool))
	suffix := fmt.Sprintf("_%08x", h.Sum32())
	return n[:MaxToolName-len(suffix)] + suffix
}

// Category maps MCP tool annotations onto a rules-engine category. Hints
// come from the server: the read-only hint lowers the category only when
// trust is set (Server.TrustAnnotations); an explicit destructive hint
// always raises it to delete; everything else (including open-world tools)
// is exec.
func Category(a *sdk.ToolAnnotations, trust bool) string {
	switch {
	case a == nil:
		return tools.CategoryExec
	case a.ReadOnlyHint && trust:
		return tools.CategoryRead
	case a.DestructiveHint != nil && *a.DestructiveHint:
		return tools.CategoryDelete
	}
	return tools.CategoryExec
}

// MaxResultBytes bounds the text returned to the model for one call.
const MaxResultBytes = 256 << 10

// ConvertResult turns an MCP tool result into a tools.Result: text content
// is joined; images, audio and binary resources become placeholders; an
// isError result becomes an error result.
func ConvertResult(r *sdk.CallToolResult) tools.Result {
	if r == nil {
		return tools.Errorf("MCP server returned no result")
	}
	var parts []string
	for _, c := range r.Content {
		switch v := c.(type) {
		case *sdk.TextContent:
			parts = append(parts, v.Text)
		case *sdk.ImageContent:
			parts = append(parts, fmt.Sprintf("[image: %s, %d bytes]", orUnknown(v.MIMEType), len(v.Data)))
		case *sdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio: %s, %d bytes]", orUnknown(v.MIMEType), len(v.Data)))
		case *sdk.ResourceLink:
			label := v.Name
			if v.Title != "" {
				label = v.Title
			}
			parts = append(parts, fmt.Sprintf("[resource link: %s <%s>]", label, v.URI))
		case *sdk.EmbeddedResource:
			if v.Resource == nil {
				continue
			}
			if v.Resource.Text != "" {
				parts = append(parts, fmt.Sprintf("[resource %s]\n%s", v.Resource.URI, v.Resource.Text))
			} else {
				parts = append(parts, fmt.Sprintf("[resource: %s, %s, %d bytes]", v.Resource.URI, orUnknown(v.Resource.MIMEType), len(v.Resource.Blob)))
			}
		default:
			b, _ := json.Marshal(c)
			parts = append(parts, fmt.Sprintf("[unsupported content: %s]", truncate(string(b), 200)))
		}
	}
	if len(parts) == 0 && r.StructuredContent != nil {
		if b, err := json.Marshal(r.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	text := strings.Join(parts, "\n")
	if len(text) > MaxResultBytes {
		text = text[:MaxResultBytes] + fmt.Sprintf("\n… [%d bytes truncated]", len(text)-MaxResultBytes)
	}
	if r.IsError && text == "" {
		text = "MCP tool reported an error"
	}
	return tools.Result{Content: text, IsError: r.IsError, Data: r.StructuredContent}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown type"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// pathWords are the property names (or description words) that clearly
// denote a file-system path.
var pathWords = map[string]bool{"path": true, "file": true, "filepath": true, "dir": true, "directory": true, "cwd": true}

var pathNameRe = regexp.MustCompile(`^(?i:(?:file|dir|directory|working|work|source|src|target|dest|destination|output|input|root|base)?[_-]?(?:path|file|file_?path|dir|directory|cwd|folder))s?$`)

var pathDescRe = regexp.MustCompile(`(?i)\b(?:file(?:system)?\s+path|path\s+to\s+(?:a|the)\s+(?:file|directory|folder)|directory\s+path|absolute\s+path|relative\s+path|working\s+directory)\b`)

// PathArgs returns the input-schema properties that are file-system paths:
// string (or string-array) properties whose name is a path word ("path",
// "file", "filepath", "dir", "directory", "cwd", or compounds such as
// "file_path", "source_dir"), or whose description plainly says so ("file
// path", "path to the file", "working directory"). Conservative: anything
// ambiguous (e.g. "url", "name", "target") is not a path.
func PathArgs(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	var out []string
	for name, v := range props {
		p, _ := v.(map[string]any)
		if p == nil || !stringish(p) {
			continue
		}
		desc, _ := p["description"].(string)
		if pathWords[strings.ToLower(name)] || pathNameRe.MatchString(name) || pathDescRe.MatchString(desc) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// stringish reports whether a schema property holds a string or strings.
func stringish(p map[string]any) bool {
	isStr := func(t any) bool {
		switch x := t.(type) {
		case string:
			return x == "string"
		case []any:
			for _, e := range x {
				if e == "string" {
					return true
				}
			}
		}
		return false
	}
	if t, ok := p["type"]; ok {
		if isStr(t) {
			return true
		}
		if t == "array" {
			if it, ok := p["items"].(map[string]any); ok {
				return isStr(it["type"])
			}
		}
		return false
	}
	return false
}

// inputSchema returns the tool's JSON schema as a map, defaulting to an
// empty object schema.
func inputSchema(v any) map[string]any {
	var m map[string]any
	switch x := v.(type) {
	case map[string]any:
		m = x
	case nil:
	default:
		if b, err := json.Marshal(x); err == nil {
			_ = json.Unmarshal(b, &m)
		}
	}
	if m == nil {
		m = map[string]any{}
	}
	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	if _, ok := m["properties"]; !ok {
		m["properties"] = map[string]any{}
	}
	return m
}

// ---------------------------------------------------------------- secrets

// refSchemes are the credential reference schemes recognised in env and
// header values. "literal:" escapes a value that would otherwise look like
// a reference.
var refSchemes = []string{"vault:", "keychain:", "env:", "mem:"}

var inlineRef = regexp.MustCompile(`\$\{((?:vault|keychain|env|mem):[^}]+)\}`)

// IsRef reports whether the whole value is a credential reference.
func IsRef(v string) bool {
	for _, s := range refSchemes {
		if strings.HasPrefix(v, s) && len(v) > len(s) {
			return true
		}
	}
	return false
}

// Resolver resolves credential references host-side.
type Resolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

var errNoCredentials = errors.New("credentials service not loaded")

// resolveValue expands a configured value: a whole-value reference
// ("vault:crewbase/github"), inline references ("Bearer ${mem:tok}"), or
// a "literal:" escape. Errors name the reference, never a value.
func resolveValue(ctx context.Context, cr Resolver, v string) (string, error) {
	if s, ok := strings.CutPrefix(v, "literal:"); ok {
		return s, nil
	}
	resolve := func(ref string) (string, error) {
		if cr == nil {
			return "", fmt.Errorf("%s: %w", ref, errNoCredentials)
		}
		val, err := cr.Resolve(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("credential %s: %w", ref, err)
		}
		return val, nil
	}
	if IsRef(v) {
		return resolve(v)
	}
	var firstErr error
	out := inlineRef.ReplaceAllStringFunc(v, func(m string) string {
		val, err := resolve(m[2 : len(m)-1])
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return val
	})
	return out, firstErr
}

// resolveMap resolves every value; secrets collects the resolved values that
// came from references or literals (for scrubbing).
func resolveMap(ctx context.Context, cr Resolver, m map[string]string, secrets *[]string) (map[string]string, error) {
	out := make(map[string]string, len(m))
	for k, v := range m {
		val, err := resolveValue(ctx, cr, v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = val
		if len(val) >= 4 {
			*secrets = append(*secrets, val)
		}
	}
	return out, nil
}

// Masked replaces configured values in API views.
const Masked = "********"

var safeRemainder = regexp.MustCompile(`^[A-Za-z][A-Za-z -]{0,19}$|^$`)

// maskValue shows credential references (they are not secrets) and hides
// literal values.
func maskValue(v string) string {
	if IsRef(v) {
		return v
	}
	if inlineRef.MatchString(v) && safeRemainder.MatchString(strings.TrimSpace(inlineRef.ReplaceAllString(v, ""))) {
		return v // e.g. "Bearer ${vault:crewbase/linear}"
	}
	return Masked
}

func maskMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = maskValue(v)
	}
	return out
}

// maskURL hides userinfo passwords and literal query values.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return Masked
	}
	changed := false
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
			changed = true
		}
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k, vs := range q {
			for i, v := range vs {
				if mv := maskValue(v); mv != v {
					vs[i], changed = "xxxxx", true
				}
			}
			q[k] = vs
		}
		u.RawQuery = q.Encode()
	}
	if !changed {
		return raw
	}
	return u.String()
}

// mergeMasked keeps the current value for keys the client echoed back
// masked (the UI never sees literal values).
func mergeMasked(cur, next map[string]string) map[string]string {
	if next == nil {
		return nil
	}
	out := make(map[string]string, len(next))
	for k, v := range next {
		if v == Masked {
			if old, ok := cur[k]; ok {
				v = old
			}
		}
		out[k] = v
	}
	return out
}

// scrub removes resolved secret values from text shown in status/errors.
func scrub(s string, secrets []string) string {
	for _, v := range secrets {
		if v == "" {
			continue
		}
		s = strings.ReplaceAll(s, v, "[REDACTED]")
		if b64 := base64.StdEncoding.EncodeToString([]byte(v)); len(v) >= 8 {
			s = strings.ReplaceAll(s, b64, "[REDACTED]")
		}
	}
	return s
}

var _ Resolver = (*credentials.Service)(nil)
