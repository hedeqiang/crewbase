// Package sdk ships the plugin SDK runtimes inside the host binary and
// scaffolds new plugins (`crew plugin new`).
//
// The host provides the SDK that matches its protocol version: before a
// process plugin starts, Materialize places @crewbase/plugin-sdk under the
// plugin's node_modules (when absent) and the crewbase_plugin Python package
// on PYTHONPATH. Plugins therefore install as plain directories with no
// dependency step. The Go SDK lives in sdk/go (package crewplugin) and is
// compiled into the plugin binary.
package sdk

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed typescript/package.json typescript/index.js typescript/index.d.ts python/crewbase_plugin/__init__.py
var files embed.FS

// ProtocolVersion is the bridge protocol spoken by the bundled SDKs.
const ProtocolVersion = 1

// writeTree copies the embedded subtree src into dst.
func writeTree(src, dst string) error {
	return fs.WalkDir(files, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}

// MaterializeNode installs the TypeScript SDK at
// <pluginDir>/node_modules/@crewbase/plugin-sdk unless the plugin ships its
// own copy.
func MaterializeNode(pluginDir string) error {
	dst := filepath.Join(pluginDir, "node_modules", "@crewbase", "plugin-sdk")
	if _, err := os.Stat(filepath.Join(dst, "package.json")); err == nil {
		return nil
	}
	return writeTree("typescript", dst)
}

// MaterializePython writes the crewbase_plugin package under dir and
// returns the directory to put on PYTHONPATH.
func MaterializePython(dir string) (string, error) {
	if err := writeTree("python", dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ---------------------------------------------------------------- scaffold

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidName reports whether name is a valid plugin name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Scaffold creates a new plugin skeleton for lang (ts | py | go) in dir
// (which must not exist or be empty). It returns the files written.
func Scaffold(name, lang, dir string) ([]string, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid plugin name %q (lowercase letters, digits, . _ -)", name)
	}
	tpl, ok := templates[lang]
	if !ok {
		return nil, fmt.Errorf("unknown language %q (want ts, py or go)", lang)
	}
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return nil, fmt.Errorf("%s is not empty", dir)
	}
	ident := strings.NewReplacer("-", "_", ".", "_").Replace(name)
	var out []string
	for file, body := range tpl {
		body = strings.NewReplacer("{{name}}", name, "{{ident}}", ident).Replace(body)
		p := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

var templates = map[string]map[string]string{
	"ts": {
		"crewbase-plugin.toml": `name = "{{name}}"
version = "0.1.0"
description = "A Crewbase plugin."
runtime = "process"
entry = ["node", "index.js"]

[inject]
services = ["tools"]

[permissions]
events = []
network = []
credentials = []
files = []
subprocess = false
`,
		"package.json": `{
  "name": "{{name}}",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "main": "index.js",
  "devDependencies": { "@crewbase/plugin-sdk": "^0.1.0" }
}
`,
		"index.js": `// @ts-check
// Plain ESM with JSDoc types: no build step. The host provides
// @crewbase/plugin-sdk at runtime. Run "crew dev ." to hot reload.
import { definePlugin } from "@crewbase/plugin-sdk";

definePlugin({
  name: "{{name}}",
  tools: {
    {{ident}}_echo: {
      description: "Echo the given text.",
      category: "none",
      params: { type: "object", properties: { text: { type: "string" } }, required: ["text"] },
      /** @param {{ text: string }} args */
      execute(args) {
        return args.text;
      },
    },
  },
});
`,
	},
	"py": {
		"crewbase-plugin.toml": `name = "{{name}}"
version = "0.1.0"
description = "A Crewbase plugin."
runtime = "process"
entry = ["python3", "main.py"]

[inject]
services = ["tools"]

[permissions]
events = []
network = []
credentials = []
files = []
subprocess = false
`,
		"main.py": `# The host provides crewbase_plugin on PYTHONPATH. Run "crew dev ." to hot reload.
from crewbase_plugin import Plugin

plugin = Plugin("{{name}}", "0.1.0")


@plugin.tool("{{ident}}_echo", description="Echo the given text.", category="none",
             params={"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]})
def echo(args, ctx):
    return args["text"]


if __name__ == "__main__":
    plugin.run()
`,
	},
	"go": {
		"crewbase-plugin.toml": `name = "{{name}}"
version = "0.1.0"
description = "A Crewbase plugin."
runtime = "process"
# Build first: go build -o plugin .
entry = ["./plugin"]

[inject]
services = ["tools"]

[permissions]
events = []
network = []
credentials = []
files = []
subprocess = false
`,
		"main.go": `package main

import (
	"context"
	"log"

	crewplugin "github.com/hedeqiang/crewbase/sdk/go"
)

func main() {
	p := crewplugin.New("{{name}}", "0.1.0")
	p.Tool(crewplugin.Tool{
		Name:        "{{ident}}_echo",
		Description: "Echo the given text.",
		Params: map[string]any{"type": "object", "required": []string{"text"},
			"properties": map[string]any{"text": map[string]any{"type": "string"}}},
		Execute: func(ctx context.Context, call crewplugin.Call, h *crewplugin.Host) (crewplugin.Result, error) {
			var a struct{ Text string }
			if err := call.Decode(&a); err != nil {
				return crewplugin.Result{}, err
			}
			return crewplugin.Text(a.Text), nil
		},
	})
	if err := p.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
`,
		"go.mod": `module {{name}}

go 1.27

require github.com/hedeqiang/crewbase v0.0.0
`,
	},
}
