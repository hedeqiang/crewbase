package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hedeqiang/crewbase/core/llm"
)

// The ACP bridge behind a CLI (an npm package such as codex-acp) is
// installed separately from the CLI and easily left behind: an old
// codex-acp offers the models of the Codex it bundles, not those of the
// installed CLI. Its version is read from the package.json next to the
// installed entrypoint (no network) and compared with the oldest version
// known to work well.

// knownBridge describes an npm bridge package.
type knownBridge struct {
	// Min is the oldest version known to work well ("" = any).
	Min string
	// ReplacedBy names the package that superseded this one.
	ReplacedBy string
	// Install is the package to install to update ("" = this one).
	Install string
}

// knownBridges are the bridges Crewbase knows, by npm package name.
var knownBridges = map[string]knownBridge{
	// 0.16 runs Codex 0.137's core and takes models and levels as session
	// config options; 0.8 (January 2026) offers gpt-5.2-codex era models
	"@zed-industries/codex-acp": {Min: "0.16.0"},
	// renamed upstream; the old package stopped at 0.12
	"@zed-industries/claude-code-acp":       {ReplacedBy: "@agentclientprotocol/claude-agent-acp", Install: "@agentclientprotocol/claude-agent-acp"},
	"@agentclientprotocol/claude-agent-acp": {Min: "0.107.0"},
}

// bridgeOf reads the bridge package behind the agent's ACP command; nil
// when it is not a known npm package (a CLI that speaks ACP itself, or
// npx, which runs the latest release).
func bridgeOf(r resolved) *llm.Bridge {
	if len(r.Command) == 0 || !filepath.IsAbs(r.Command[0]) {
		return nil
	}
	p, err := filepath.EvalSymlinks(r.Command[0])
	if err != nil {
		return nil
	}
	dir := filepath.Dir(p)
	for i := 0; i < 5 && dir != filepath.Dir(dir); i, dir = i+1, filepath.Dir(dir) {
		b, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			continue
		}
		var pkg struct{ Name, Version string }
		if json.Unmarshal(b, &pkg) != nil || pkg.Name == "" {
			continue
		}
		kb, ok := knownBridges[pkg.Name]
		if !ok {
			return nil
		}
		out := &llm.Bridge{Package: pkg.Name, Version: pkg.Version, MinVersion: kb.Min, ReplacedBy: kb.ReplacedBy}
		out.Outdated = kb.ReplacedBy != "" || (kb.Min != "" && versionLess(pkg.Version, kb.Min))
		if out.Outdated {
			inst := kb.Install
			if inst == "" {
				inst = pkg.Name
			}
			out.UpdateCommand = "npm install -g " + inst + "@latest"
		}
		return out
	}
	return nil
}

// versionLess compares dotted numeric versions (pre-release tags ignored).
func versionLess(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, f := range strings.Split(v, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

// versionKey identifies the CLI and bridge a model list comes from.
func versionKey(cli string, b *llm.Bridge) string {
	k := "cli " + cli
	if b != nil {
		k += " | " + b.Package + " " + b.Version
	}
	return k
}

// Bridge reports the agent's ACP bridge package (nil if not a known one).
func (a *Adapter) Bridge() *llm.Bridge {
	if a.bridge == nil {
		return nil
	}
	b := *a.bridge
	return &b
}
