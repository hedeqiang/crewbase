// Command notices writes THIRD_PARTY_NOTICES.txt: the license of every
// third-party component built into Crewbase — the Go modules linked into
// the binaries (on macOS, Linux and Windows), the Go standard library, and
// the npm packages bundled into the web UI.
//
//	go run ./internal/notices          # rewrite the files
//	go run ./internal/notices -check   # fail when they are out of date (CI)
//
// The file is written to the repository root and to apps/web/public, so it
// ships inside every build of the UI (crewd, the desktop app) at
// /THIRD_PARTY_NOTICES.txt; release archives carry the root copy.
// Run `pnpm install` in apps/web first.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var outputs = []string{"THIRD_PARTY_NOTICES.txt", "apps/web/public/THIRD_PARTY_NOTICES.txt"}

type component struct {
	Name, Version, URL string
	Texts              []string // license and notice files, in order
}

func main() {
	check := flag.Bool("check", false, "fail when the files are out of date")
	flag.Parse()
	if err := run(*check); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

func run(check bool) error {
	goComps, err := goModules()
	if err != nil {
		return err
	}
	webComps, err := npmPackages("apps/web")
	if err != nil {
		return err
	}
	out := render(goComps, webComps)
	var stale []string
	for _, p := range outputs {
		if check {
			b, _ := os.ReadFile(p)
			if !bytes.Equal(b, out) {
				stale = append(stale, p)
				if len(stale) == 1 {
					firstDiff(b, out)
				}
			}
			continue
		}
		if err := os.WriteFile(p, out, 0o644); err != nil {
			return err
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("%s out of date: run `go run ./internal/notices`", strings.Join(stale, ", "))
	}
	if !check {
		fmt.Printf("%d Go modules, %d npm packages\n", len(goComps), len(webComps))
	}
	return nil
}

// ---------------------------------------------------------------- Go

var licenseFile = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents)(\.(md|txt|markdown|rst))?$|^licen[cs]e[-_.]`)

func licenseTexts(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && licenseFile.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	// LICENSE before NOTICE / PATENTS
	sort.Slice(names, func(i, j int) bool {
		ri, rj := rank(names[i]), rank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	var out []string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err == nil && len(bytes.TrimSpace(b)) > 0 {
			out = append(out, strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")))
		}
	}
	return out
}

func rank(name string) int {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "licen"), strings.HasPrefix(n, "copying"):
		return 0
	case strings.HasPrefix(n, "notice"):
		return 1
	}
	return 2
}

func goModules() ([]component, error) {
	type mod struct{ Path, Version, Dir string }
	mods := map[string]mod{}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		cmd := exec.Command("go", "list", "-deps", "-f", `{{with .Module}}{{if not .Main}}{{.Path}}	{{.Version}}	{{.Dir}}{{end}}{{end}}`, "./apps/...")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		cmd.Stderr = os.Stderr
		b, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list (%s): %w", goos, err)
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			f := strings.Split(sc.Text(), "\t")
			if len(f) == 3 && f[0] != "" {
				mods[f[0]] = mod{f[0], f[1], f[2]}
			}
		}
	}
	var out []component
	var missing []string
	for _, m := range mods {
		texts := licenseTexts(m.Dir)
		if len(texts) == 0 {
			missing = append(missing, m.Path)
			continue
		}
		out = append(out, component{Name: m.Path, Version: m.Version, URL: "https://pkg.go.dev/" + m.Path, Texts: texts})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("no license file in Go modules: %s", strings.Join(missing, ", "))
	}
	// the standard library and runtime are linked into every binary
	root, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return nil, err
	}
	ver, _ := exec.Command("go", "env", "GOVERSION").Output()
	// only LICENSE: some Go distributions add files (Homebrew keeps PATENTS)
	// and the output must not depend on how Go was installed
	goroot := strings.TrimSpace(string(root))
	lic, err := os.ReadFile(filepath.Join(goroot, "LICENSE"))
	if err != nil { // Homebrew keeps it next to GOROOT (libexec)
		lic, err = os.ReadFile(filepath.Join(goroot, "..", "LICENSE"))
	}
	if err != nil {
		return nil, errors.New("no LICENSE in GOROOT")
	}
	std := component{Name: "Go standard library", Version: strings.TrimSpace(string(ver)), URL: "https://go.dev", Texts: []string{strings.TrimSpace(string(lic))}}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return append([]component{std}, out...), nil
}

// ---------------------------------------------------------------- npm

type pkgJSON struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	License      any               `json:"license"`
	Author       any               `json:"author"`
	Homepage     string            `json:"homepage"`
	Repository   any               `json:"repository"`
	Dependencies map[string]string `json:"dependencies"`
}

func readPkg(dir string) (pkgJSON, error) {
	var p pkgJSON
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

// resolve finds dep the way Node does from a package's real directory
// (pnpm keeps a package's dependencies next to it).
func resolve(from, dep string) (string, error) {
	for d := from; ; d = filepath.Dir(d) {
		var c string
		if filepath.Base(d) == "node_modules" {
			c = filepath.Join(d, dep)
		} else {
			c = filepath.Join(d, "node_modules", dep)
		}
		if st, err := os.Stat(filepath.Join(c, "package.json")); err == nil && !st.IsDir() {
			return filepath.EvalSymlinks(c)
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("cannot resolve %s from %s (run pnpm install in apps/web)", dep, from)
		}
	}
}

func npmPackages(app string) ([]component, error) {
	root, err := filepath.Abs(app)
	if err != nil {
		return nil, err
	}
	top, err := readPkg(root)
	if err != nil {
		return nil, err
	}
	seen := map[string]component{}
	var walk func(dir string, deps map[string]string) error
	walk = func(dir string, deps map[string]string) error {
		names := make([]string, 0, len(deps))
		for n := range deps {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			pd, err := resolve(dir, n)
			if err != nil {
				return err
			}
			p, err := readPkg(pd)
			if err != nil {
				return err
			}
			key := p.Name + "@" + p.Version
			if _, ok := seen[key]; ok {
				continue
			}
			texts := licenseTexts(pd)
			if len(texts) == 0 {
				t, err := declaredLicense(p)
				if err != nil {
					return err
				}
				texts = []string{t}
			}
			seen[key] = component{Name: p.Name, Version: p.Version, URL: npmURL(p), Texts: texts}
			if err := walk(pd, p.Dependencies); err != nil {
				return err
			}
		}
		return nil
	}
	// only runtime dependencies are bundled into the UI
	if err := walk(root, top.Dependencies); err != nil {
		return nil, err
	}
	out := make([]component, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func npmURL(p pkgJSON) string {
	if p.Homepage != "" {
		return p.Homepage
	}
	return "https://www.npmjs.com/package/" + p.Name
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if s, ok := x["name"].(string); ok {
			return s
		}
		if s, ok := x["type"].(string); ok {
			return s
		}
	}
	return ""
}

// declaredLicense builds the notice of a package that ships no license
// file from its package.json (only MIT, whose text is fixed).
func declaredLicense(p pkgJSON) (string, error) {
	lic, author := str(p.License), str(p.Author)
	if lic != "MIT" || author == "" {
		return "", fmt.Errorf("%s@%s has no license file and declares %q by %q; add its notice by hand", p.Name, p.Version, lic, author)
	}
	return "MIT License (declared in package.json; the package ships no license file)\n\nCopyright (c) " + author + "\n\n" + mitText, nil
}

const mitText = `Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`

// ---------------------------------------------------------------- output

func render(goComps, webComps []component) []byte {
	var b bytes.Buffer
	rule := strings.Repeat("=", 78)
	b.WriteString("THIRD-PARTY NOTICES\n\n")
	b.WriteString("Crewbase (https://github.com/hedeqiang/crewbase) is licensed under the\n")
	b.WriteString("MIT License. It includes the third-party components below, each\n")
	b.WriteString("under its own license. Trademarks and logos (including the model vendor\n")
	b.WriteString("icons from LobeHub Icons) belong to their owners and only identify them.\n")
	b.WriteString("The chat app marks in apps/web/src/assets/im are the platforms' own,\n")
	b.WriteString("taken from their official brand pages (Slack: slack.com; Discord:\n")
	b.WriteString("discord.com/branding; Telegram: telegram.org; Feishu / Lark: feishu.cn,\n")
	b.WriteString("larksuite.com) and shown only to label the integration with each app.\n\n")
	b.WriteString("Generated by `go run ./internal/notices`; do not edit by hand.\n\n")
	section := func(title string, cs []component) {
		b.WriteString(rule + "\n" + title + "\n" + rule + "\n\n")
		for _, c := range cs {
			b.WriteString(strings.Repeat("-", 78) + "\n")
			fmt.Fprintf(&b, "%s %s\n%s\n\n", c.Name, c.Version, c.URL)
			for i, t := range c.Texts {
				if i > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(t + "\n")
			}
			b.WriteString("\n")
		}
	}
	section("Go modules (crewd, crew, crew-relay and the desktop app)", goComps)
	section("Web UI (bundled npm packages)", webComps)
	return b.Bytes()
}

// firstDiff prints where the committed file and the fresh one part.
func firstDiff(old, cur []byte) {
	a, b := strings.Split(string(old), "\n"), strings.Split(string(cur), "\n")
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			fmt.Fprintf(os.Stderr, "first difference at line %d:\n  committed: %q\n  generated: %q\n", i+1, x, y)
			return
		}
	}
}
