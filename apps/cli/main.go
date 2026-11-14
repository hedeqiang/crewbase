// Command crew is the Crewbase command line client.
//
//	crew plugins [--state pending]
//	crew agents
//	crew agent new <name> [--role ...] [--model provider/model ...]
//	crew chat <agent> <message...>      send and stream until the turn ends
//	crew approvals
//	crew approve <callId> | crew reject <callId> [reason]
//	crew dump-config [--profile desktop]
//	crew daemon [crewd flags]            run the daemon in-process
//	crew plugin new <name> --lang ts|py|go [--dir d]   scaffold a process plugin
//	crew plugin install <path|tarball|url|npm-name> [--no-enable]
//	crew plugin approve <name> [--trust-future] [--yes]
//	crew plugin list|market
//	crew plugin enable|disable|reload|remove <name>
//	crew dev <dir> [--approve] [--interval 500ms]   install, watch, hot reload
//	crew doctor [--profile server] [--offline] [--health]   readiness checks
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/hedeqiang/crewbase/capabilities/sandbox"
	"github.com/hedeqiang/crewbase/providers/sandbox/bubblewrap"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/hedeqiang/crewbase/apps/launch"
	// Process plugin runtime + plugin manager, so `crew daemon` can enable
	// them with a patch entry until bundles include them.
	_ "github.com/hedeqiang/crewbase/features/plugins"
	"github.com/hedeqiang/crewbase/kernel"
	"github.com/hedeqiang/crewbase/providers/browser/chrome"
	dockersb "github.com/hedeqiang/crewbase/providers/sandbox/docker"
	"github.com/hedeqiang/crewbase/sdk"
)

var (
	addr  = env("CREWBASE_ADDR", "http://127.0.0.1:7788")
	root  = launch.DefaultRoot()
	token string
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	if b, err := os.ReadFile(filepath.Join(root, "token")); err == nil {
		token = strings.TrimSpace(string(b))
	}
	if v := os.Getenv("CREWBASE_TOKEN"); v != "" {
		token = v
	}
	var err error
	switch cmd {
	case "plugins":
		err = plugins(args)
	case "agents":
		err = agents()
	case "agent":
		err = agentCmd(args)
	case "chat":
		err = chat(args)
	case "approvals":
		err = approvals()
	case "approve", "reject":
		err = decide(cmd, args)
	case "dump-config":
		err = dumpConfig(args)
	case "daemon":
		err = daemon(args)
	case "plugin":
		err = pluginCmd(args)
	case "dev":
		err = devCmd(args)
	case "doctor":
		err = doctor(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "crew:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: crew plugins|plugin|dev|agents|agent new|chat|approvals|approve|reject|dump-config|daemon|doctor")
	os.Exit(2)
}

func call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, addr+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach crewd at %s (is it running?): %w", addr, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return errors.New(e.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func plugins(args []string) error {
	fs := flag.NewFlagSet("plugins", flag.ExitOnError)
	state := fs.String("state", "", "filter by state (active|pending|failed)")
	fs.Parse(args)
	var out struct {
		Plugins []kernel.PluginInfo `json:"plugins"`
	}
	if err := call("GET", "/api/plugins?state="+*state, nil, &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATE\tPROVIDES\tMISSING / ERROR")
	for _, p := range out.Plugins {
		extra := strings.Join(p.Missing, ",")
		if p.Error != "" {
			extra = p.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.State, strings.Join(p.Provides, ","), extra)
	}
	return tw.Flush()
}

type agentView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Status string `json:"status"`
	Mode   string `json:"effectiveMode"`
	Models []struct {
		Provider, Model string
	} `json:"models"`
	Session string `json:"session"`
}

func agents() error {
	var out struct {
		Agents []agentView `json:"agents"`
	}
	if err := call("GET", "/api/agents", nil, &out); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tMODE\tMODELS")
	for _, a := range out.Agents {
		var ms []string
		for _, m := range a.Models {
			ms = append(ms, m.Provider+"/"+m.Model)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.ID, a.Name, a.Status, a.Mode, strings.Join(ms, " → "))
	}
	return tw.Flush()
}

type multi []string

func (m *multi) String() string     { return fmt.Sprint(*m) }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func agentCmd(args []string) error {
	if len(args) < 2 || args[0] != "new" {
		return errors.New("usage: crew agent new <name> [--role r] [--model p/m ...]")
	}
	fs := flag.NewFlagSet("agent new", flag.ExitOnError)
	role := fs.String("role", "", "what this agent is responsible for")
	var models multi
	fs.Var(&models, "model", "model ref provider/model[@effort] (repeatable, in order)")
	fs.Parse(args[2:])
	var a agentView
	if err := call("POST", "/api/agents", map[string]any{"name": args[1], "role": *role, "modelRefs": models}, &a); err != nil {
		return err
	}
	fmt.Printf("created %s (%s)\n", a.Name, a.ID)
	return nil
}

func chat(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: crew chat <agent> <message...>")
	}
	id, text := args[0], strings.Join(args[1:], " ")
	// subscribe first so nothing is missed
	req, _ := http.NewRequest("GET", addr+"/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("events: %s", resp.Status)
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := call("POST", "/api/agents/"+id+"/messages", map[string]any{"text": text}, &in); err != nil {
		return err
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var typ string
	claimed := false
	for sc.Scan() {
		line := sc.Text()
		if t, ok := strings.CutPrefix(line, "event: "); ok {
			typ = t
			continue
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		switch typ {
		case "session.event":
			var e struct {
				Type  string          `json:"type"`
				Agent string          `json:"agent"`
				Data  json.RawMessage `json:"data"`
			}
			json.Unmarshal([]byte(data), &e)
			if e.Agent != id && e.Type != "message/user" {
				continue
			}
			switch e.Type {
			case "input/claimed":
				var m map[string]string
				json.Unmarshal(e.Data, &m)
				claimed = claimed || m["id"] == in.ID
			case "model/switched":
				var m struct {
					From, To struct{ Provider, Model string }
					Reason   string
				}
				json.Unmarshal(e.Data, &m)
				fmt.Printf("· switched %s → %s (%s)\n", m.From.Provider, m.To.Provider, m.Reason)
			case "tool/call":
				var m struct {
					Call struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"call"`
				}
				json.Unmarshal(e.Data, &m)
				fmt.Printf("· %s %s\n", m.Call.Name, m.Call.Args)
			case "approval/asked":
				var m struct {
					CallID  string `json:"callId"`
					Summary string `json:"summary"`
					Reason  string `json:"reason"`
				}
				json.Unmarshal(e.Data, &m)
				fmt.Printf("⏸ needs approval: %s (%s)\n  crew approve %s\n", m.Summary, m.Reason, m.CallID)
			case "message/assistant":
				var m struct {
					Message struct {
						Parts []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"message"`
					Model struct{ Provider, Model string } `json:"model"`
				}
				json.Unmarshal(e.Data, &m)
				for _, p := range m.Message.Parts {
					if p.Type == "text" && p.Text != "" {
						fmt.Printf("%s\n", p.Text)
					}
				}
			case "turn/end":
				if claimed {
					var m struct {
						Status, Error string
					}
					json.Unmarshal(e.Data, &m)
					if m.Status != "completed" {
						return fmt.Errorf("turn %s: %s", m.Status, m.Error)
					}
					return nil
				}
			}
		}
	}
	return sc.Err()
}

func approvals() error {
	var out struct {
		Pending []struct {
			CallID, Agent, Tool, Category, Summary, Reason string
			Asked                                          time.Time
		} `json:"pending"`
	}
	if err := call("GET", "/api/approvals", nil, &out); err != nil {
		return err
	}
	if len(out.Pending) == 0 {
		fmt.Println("nothing needs you")
	}
	for _, p := range out.Pending {
		fmt.Printf("%s  %s wants to %s  [%s: %s]\n", p.CallID, p.Agent, p.Summary, p.Category, p.Reason)
	}
	return nil
}

func decide(cmd string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: crew %s <callId>", cmd)
	}
	body := map[string]any{"outcome": "allowed-once"}
	if cmd == "reject" {
		body = map[string]any{"outcome": "rejected", "message": strings.Join(args[1:], " ")}
	}
	return call("POST", "/api/approvals/"+url.PathEscape(args[0]), body, nil)
}

func dumpConfig(args []string) error {
	fs := flag.NewFlagSet("dump-config", flag.ExitOnError)
	profile := fs.String("profile", "desktop", "profile")
	fs.Parse(args)
	entries, err := launch.Resolve(launch.Options{Profile: *profile})
	if err != nil {
		return err
	}
	fmt.Print(kernel.DumpConfig(entries))
	return nil
}

func daemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	var o launch.Options
	fs.StringVar(&o.Profile, "profile", "desktop", "profile")
	fs.StringVar(&o.Root, "root", "", "data directory")
	fs.StringVar(&o.Addr, "addr", "", "listen address")
	fs.Parse(args)
	h, err := launch.Start(o)
	if err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	h.Close()
	return nil
}

// ---------------------------------------------------------------- process plugins

type installedPlugin struct {
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Description   string   `json:"description"`
	Source        string   `json:"source"`
	State         string   `json:"state"`
	Error         string   `json:"error"`
	Enabled       bool     `json:"enabled"`
	Approved      bool     `json:"approved"`
	NeedsApproval []string `json:"needsApproval"`
	Requested     struct {
		Services, Events, Network, Credentials, Files []string
		Subprocess                                    bool
	} `json:"requested"`
	Process *struct {
		PID         int      `json:"pid"`
		Restarts    int      `json:"restarts"`
		Tools       []string `json:"tools"`
		Events      []string `json:"events"`
		Enforcement string   `json:"enforcement"`
	} `json:"process"`
}

// splitFlags parses flags that may follow positional arguments.
func splitFlags(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			fs.Parse(args)
			args = fs.Args()
			continue
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos
}

func printPlugin(p installedPlugin) {
	fmt.Printf("%s %s  %s\n", p.Name, p.Version, p.State)
	if p.Error != "" {
		fmt.Printf("  error: %s\n", p.Error)
	}
	if len(p.NeedsApproval) > 0 {
		fmt.Printf("  needs approval: %s\n  crew plugin approve %s\n", strings.Join(p.NeedsApproval, ", "), p.Name)
	}
	if p.Process != nil {
		fmt.Printf("  pid %d, restarts %d, tools [%s], events [%s], enforcement %s\n", p.Process.PID, p.Process.Restarts,
			strings.Join(p.Process.Tools, " "), strings.Join(p.Process.Events, " "), p.Process.Enforcement)
	}
}

func printRequested(p installedPlugin) {
	r := p.Requested
	fmt.Printf("%s %s requests (it can do everything it declares; it is not \"safe\" code):\n", p.Name, p.Version)
	line := func(k string, v []string) {
		if len(v) > 0 {
			fmt.Printf("  %-12s %s\n", k, strings.Join(v, ", "))
		}
	}
	line("services", r.Services)
	line("events", r.Events)
	line("network", r.Network)
	line("credentials", r.Credentials)
	line("files", r.Files)
	if r.Subprocess {
		fmt.Printf("  %-12s yes\n", "subprocess")
	}
}

func confirm(q string) bool {
	fmt.Print(q + " [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func pluginCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: crew plugin new|install|approve|list|market|enable|disable|reload|remove ...")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "new":
		fs := flag.NewFlagSet("plugin new", flag.ExitOnError)
		lang := fs.String("lang", "ts", "ts | py | go")
		fs.String("runtime", "process", "plugin runtime (only process)")
		dir := fs.String("dir", "", "target directory (default ./<name>)")
		pos := splitFlags(fs, args)
		if len(pos) != 1 {
			return errors.New("usage: crew plugin new <name> --lang ts|py|go [--dir d]")
		}
		d := *dir
		if d == "" {
			d = pos[0]
		}
		files, err := sdk.Scaffold(pos[0], *lang, d)
		if err != nil {
			return err
		}
		for _, f := range files {
			fmt.Println("created", f)
		}
		fmt.Printf("next: crew dev %s\n", d)
		return nil
	case "install":
		fs := flag.NewFlagSet("plugin install", flag.ExitOnError)
		noEnable := fs.Bool("no-enable", false, "install without enabling")
		pos := splitFlags(fs, args)
		if len(pos) != 1 {
			return errors.New("usage: crew plugin install <path|tarball|url|npm-name>")
		}
		src := pos[0]
		if st, err := os.Stat(src); err == nil && st != nil {
			src, _ = filepath.Abs(src)
		}
		enable := !*noEnable
		var p installedPlugin
		if err := call("POST", "/api/plugins/install", map[string]any{"source": src, "enable": enable}, &p); err != nil {
			return err
		}
		if !p.Approved {
			printRequested(p)
		}
		printPlugin(p)
		return nil
	case "approve":
		fs := flag.NewFlagSet("plugin approve", flag.ExitOnError)
		trust := fs.Bool("trust-future", false, "trust future versions that ask for no new permissions")
		yes := fs.Bool("yes", false, "do not ask for confirmation")
		pos := splitFlags(fs, args)
		if len(pos) != 1 {
			return errors.New("usage: crew plugin approve <name> [--trust-future] [--yes]")
		}
		p, err := findPlugin(pos[0])
		if err != nil {
			return err
		}
		printRequested(p)
		if !*yes && !confirm("Approve these permissions?") {
			return errors.New("not approved")
		}
		url := "/api/plugins/" + p.Name + "/approve"
		p = installedPlugin{}
		if err := call("POST", url, map[string]any{"trustFutureVersions": *trust}, &p); err != nil {
			return err
		}
		printPlugin(p)
		return nil
	case "list":
		var out struct {
			Plugins []installedPlugin `json:"plugins"`
		}
		if err := call("GET", "/api/plugins/installed", nil, &out); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tVERSION\tSTATE\tTOOLS\tNEEDS / ERROR")
		for _, p := range out.Plugins {
			var tools []string
			if p.Process != nil {
				tools = p.Process.Tools
			}
			extra := strings.Join(p.NeedsApproval, ",")
			if p.Error != "" {
				extra = p.Error
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Version, p.State, strings.Join(tools, ","), extra)
		}
		return tw.Flush()
	case "market":
		var out struct {
			Plugins []struct {
				Name, Description, Version, Source, Installed string
			} `json:"plugins"`
		}
		if err := call("GET", "/api/market", nil, &out); err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tVERSION\tINSTALLED\tDESCRIPTION")
		for _, p := range out.Plugins {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Version, p.Installed, p.Description)
		}
		return tw.Flush()
	case "enable", "disable", "reload":
		if len(args) != 1 {
			return fmt.Errorf("usage: crew plugin %s <name>", sub)
		}
		var p installedPlugin
		if err := call("POST", "/api/plugins/"+args[0]+"/"+sub, nil, &p); err != nil {
			return err
		}
		printPlugin(p)
		return nil
	case "remove", "uninstall":
		if len(args) != 1 {
			return errors.New("usage: crew plugin remove <name>")
		}
		return call("DELETE", "/api/plugins/"+args[0], nil, nil)
	}
	return fmt.Errorf("unknown plugin command %q", sub)
}

func findPlugin(name string) (installedPlugin, error) {
	var out struct {
		Plugins []installedPlugin `json:"plugins"`
	}
	if err := call("GET", "/api/plugins/installed", nil, &out); err != nil {
		return installedPlugin{}, err
	}
	for _, p := range out.Plugins {
		if p.Name == name {
			return p, nil
		}
	}
	return installedPlugin{}, fmt.Errorf("plugin %q is not installed", name)
}

// snapshotTree records size+mtime of every source file (no new deps: polling).
func snapshotTree(dir string) map[string]string {
	out := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if p != dir && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "__pycache__") {
				return filepath.SkipDir
			}
			return nil
		}
		out[p] = fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// devCmd installs a plugin from a working directory, then watches it and
// hot reloads (dispose + start) on every change.
func devCmd(args []string) error {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	approve := fs.Bool("approve", false, "approve the manifest permissions without asking")
	interval := fs.Duration("interval", 500*time.Millisecond, "poll interval")
	pos := splitFlags(fs, args)
	if len(pos) > 1 {
		return errors.New("usage: crew dev [dir] [--approve] [--interval 500ms]")
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	var p installedPlugin
	if err := call("POST", "/api/plugins/install", map[string]any{"source": dir, "enable": true}, &p); err != nil {
		return err
	}
	if !p.Approved {
		printRequested(p)
		if !*approve && !confirm("Approve these permissions?") {
			return errors.New("not approved")
		}
		url := "/api/plugins/" + p.Name + "/approve"
		p = installedPlugin{}
		if err := call("POST", url, map[string]any{}, &p); err != nil {
			return err
		}
	}
	printPlugin(p)
	fmt.Printf("watching %s (ctrl-c to stop)\n", dir)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	last := snapshotTree(dir)
	tick := time.NewTicker(*interval)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			return nil
		case <-tick.C:
		}
		cur := snapshotTree(dir)
		if sameTree(cur, last) {
			continue
		}
		// wait for the editor to finish writing
		time.Sleep(*interval / 2)
		cur = snapshotTree(dir)
		last = cur
		fmt.Printf("%s change detected, reloading %s\n", time.Now().Format("15:04:05"), p.Name)
		var np installedPlugin
		if err := call("POST", "/api/plugins/"+p.Name+"/reload", nil, &np); err != nil {
			fmt.Fprintln(os.Stderr, "reload failed:", err)
			continue
		}
		if !np.Approved {
			printRequested(np)
			if *approve || confirm("The plugin asks for new permissions. Approve?") {
				url := "/api/plugins/" + np.Name + "/approve"
				np = installedPlugin{}
				if err := call("POST", url, map[string]any{}, &np); err != nil {
					fmt.Fprintln(os.Stderr, "approve failed:", err)
				}
			}
		}
		printPlugin(np)
	}
}

// ---------------------------------------------------------------- doctor

// doctorReport collects check results: ok, warn or fail.
type doctorReport struct{ fails, warns int }

func (d *doctorReport) line(level, what, detail string) {
	switch level {
	case "fail":
		d.fails++
	case "warn":
		d.warns++
	}
	fmt.Printf("[%-4s] %-18s %s\n", level, what, detail)
}

func entryByID(entries []kernel.Entry, id string) (kernel.Entry, bool) {
	for _, e := range entries {
		if e.ID == id && !e.Disabled {
			return e, true
		}
	}
	return kernel.Entry{}, false
}

func cfgString(e kernel.Entry, key string) string {
	s, _ := e.Config[key].(string)
	return s
}

// doctor checks that this machine is ready to run a profile (default
// server): sandbox, browser, data directory, gateway binding and, when the
// daemon is running, model provider reachability.
//
//	crew doctor [--profile server] [--root dir] [--offline] [--health]
//
// --health only asks the running daemon for /api/health (container
// healthchecks). The exit status is non-zero when a check fails.
func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	profile := fs.String("profile", "server", "profile to check")
	dataDir := fs.String("root", root, "data directory")
	offline := fs.Bool("offline", false, "skip checks that need the running daemon")
	health := fs.Bool("health", false, "only check that the daemon answers /api/health")
	var patches multi
	fs.Var(&patches, "patch", "patch file the daemon runs with (repeatable; CREWBASE_PATCH also applies)")
	fs.Parse(args)
	if *dataDir != root {
		if b, err := os.ReadFile(filepath.Join(*dataDir, "token")); err == nil && os.Getenv("CREWBASE_TOKEN") == "" {
			token = strings.TrimSpace(string(b))
		}
	}
	if *health {
		var out struct {
			OK bool `json:"ok"`
		}
		if err := call("GET", "/api/health", nil, &out); err != nil {
			return err
		}
		if !out.OK {
			return errors.New("daemon reports unhealthy")
		}
		fmt.Println("ok")
		return nil
	}
	rep := &doctorReport{}
	fmt.Printf("crew doctor — profile %s, data %s (%s/%s)\n\n", *profile, *dataDir, runtime.GOOS, runtime.GOARCH)
	entries, err := launch.Resolve(launch.Options{Profile: *profile, Root: *dataDir, Patches: patches})
	if err != nil {
		rep.line("fail", "config", err.Error())
		return fmt.Errorf("%d check(s) failed", rep.fails)
	}
	rep.line("ok", "config", fmt.Sprintf("%d plugin entries resolved", len(entries)))
	doctorDataDir(rep, *dataDir)
	doctorSandbox(rep, entries)
	doctorBrowser(rep, entries)
	doctorGateway(rep, entries)
	if *offline {
		rep.line("warn", "daemon", "skipped (--offline)")
	} else {
		doctorDaemon(rep)
	}
	fmt.Printf("\n%d failed, %d warnings\n", rep.fails, rep.warns)
	if rep.fails > 0 {
		return fmt.Errorf("%d check(s) failed", rep.fails)
	}
	return nil
}

func doctorDataDir(rep *doctorReport, dir string) {
	st, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		rep.line("warn", "data dir", dir+" does not exist yet (crewd creates it with 0700)")
		return
	case err != nil:
		rep.line("fail", "data dir", err.Error())
		return
	case !st.IsDir():
		rep.line("fail", "data dir", dir+" is not a directory")
		return
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		rep.line("warn", "data dir", fmt.Sprintf("%s is %04o; other users can read it (chmod 700)", dir, perm))
	} else {
		rep.line("ok", "data dir", fmt.Sprintf("%s (%04o)", dir, perm))
	}
	probe := filepath.Join(dir, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		rep.line("fail", "data dir write", err.Error())
	} else {
		os.Remove(probe)
	}
	if st, err := os.Stat(filepath.Join(dir, "token")); err == nil {
		if st.Mode().Perm()&0o077 != 0 {
			rep.line("fail", "token file", fmt.Sprintf("token is %04o; must be 0600", st.Mode().Perm()))
		} else {
			rep.line("ok", "token file", "0600")
		}
	}
	for _, sub := range []string{"sessions", "audit"} {
		if st, err := os.Stat(filepath.Join(dir, sub)); err == nil && st.Mode().Perm()&0o077 != 0 {
			rep.line("warn", sub, fmt.Sprintf("%04o; other users can read it", st.Mode().Perm()))
		}
	}
}

func doctorSandbox(rep *doctorReport, entries []kernel.Entry) {
	e, ok := entryByID(entries, "sandbox")
	if !ok {
		rep.line("fail", "sandbox", "no sandbox entry: sandbox-mode commands cannot run")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch e.Name {
	case "@crewbase/sandbox-docker":
		var cfg dockersb.Config
		if b, err := json.Marshal(e.Config); err == nil {
			_ = json.Unmarshal(b, &cfg)
		}
		d := dockersb.New(cfg)
		if err := d.Available(ctx); err != nil {
			rep.line("fail", "sandbox", "docker: "+err.Error())
			return
		}
		rep.line("ok", "sandbox", "docker daemon reachable, image "+d.Image())
		if cfg.SeccompProfile == "" {
			rep.line("warn", "sandbox seccomp", "no seccompProfile configured (Docker's default profile applies)")
		}
	case "@crewbase/sandbox-seatbelt":
		if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
			rep.line("fail", "sandbox", "seatbelt: /usr/bin/sandbox-exec not found")
			return
		}
		out, err := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").CombinedOutput()
		if err != nil {
			rep.line("fail", "sandbox", fmt.Sprintf("seatbelt probe failed: %v %s", err, strings.TrimSpace(string(out))))
			return
		}
		rep.line("ok", "sandbox", "seatbelt (sandbox-exec) works")
	case "@crewbase/sandbox-namespace":
		b := bubblewrap.New(bubblewrap.Config{})
		if _, err := b.Check(ctx, sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, Network: sandbox.NetworkNone}); err != nil {
			rep.line("fail", "sandbox", "bubblewrap: "+err.Error())
			return
		}
		rep.line("ok", "sandbox", "bubblewrap (Linux namespaces) works")
	case "@crewbase/sandbox-none":
		rep.line("warn", "sandbox", "sandbox-none: commands run unconfined")
	default:
		rep.line("warn", "sandbox", "unknown provider "+e.Name+" (not checked)")
	}
}

func doctorBrowser(rep *doctorReport, entries []kernel.Entry) {
	e, ok := entryByID(entries, "browser")
	if !ok {
		rep.line("ok", "browser", "disabled")
		return
	}
	if cfgString(e, "attach") != "" {
		rep.line("ok", "browser", "attaches to "+cfgString(e, "attach"))
		return
	}
	exe := cfgString(e, "executable")
	if exe == "" {
		exe = chrome.FindExecutable()
	}
	if exe == "" {
		rep.line("warn", "browser", "no Chrome/Chromium found (set CREWBASE_BROWSER or browser.executable); browser tools will fail")
		return
	}
	if st, err := os.Stat(exe); err != nil || st.Mode()&0o111 == 0 {
		rep.line("fail", "browser", exe+" is not executable")
		return
	}
	rep.line("ok", "browser", exe)
}

func doctorGateway(rep *doctorReport, entries []kernel.Entry) {
	e, ok := entryByID(entries, "gateway")
	if !ok {
		rep.line("ok", "gateway", "not configured (no API)")
		return
	}
	a := cfgString(e, "addr")
	if a == "" {
		a = "127.0.0.1:7788"
	}
	host, _, err := net.SplitHostPort(a)
	if err != nil {
		rep.line("fail", "gateway", fmt.Sprintf("bad addr %q: %v", a, err))
		return
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		rep.line("ok", "gateway", a+" (loopback only)")
		return
	}
	rep.line("warn", "gateway", a+" is not loopback: never expose it publicly; use an SSH tunnel, Tailscale or the relay (docs/deploy.md)")
}

func doctorDaemon(rep *doctorReport) {
	var h struct {
		OK bool `json:"ok"`
	}
	if err := call("GET", "/api/health", nil, &h); err != nil {
		rep.line("warn", "daemon", err.Error())
		return
	}
	rep.line("ok", "daemon", "answering at "+addr)
	var plugins struct {
		Plugins []kernel.PluginInfo `json:"plugins"`
	}
	if err := call("GET", "/api/plugins", nil, &plugins); err == nil {
		for _, p := range plugins.Plugins {
			if p.State == kernel.StateFailed {
				rep.line("warn", "plugin", p.ID+" FAILED: "+p.Error)
			}
		}
	}
	var models struct {
		Providers []struct {
			ID     string            `json:"id"`
			Kind   string            `json:"kind"`
			Error  string            `json:"error"`
			Models []json.RawMessage `json:"models"`
		} `json:"providers"`
	}
	if err := call("GET", "/api/models", nil, &models); err != nil {
		rep.line("warn", "models", err.Error())
		return
	}
	usable := 0
	for _, p := range models.Providers {
		switch {
		case p.Error != "":
			rep.line("warn", "model "+p.ID, p.Error)
		case len(p.Models) == 0:
			rep.line("warn", "model "+p.ID, "reachable but lists no models")
		default:
			usable++
			rep.line("ok", "model "+p.ID, fmt.Sprintf("%s, %d models", p.Kind, len(p.Models)))
		}
	}
	if usable == 0 {
		rep.line("fail", "models", "no reachable model provider: add one (e.g. deploy/aliyun/crewbase.patch.yml)")
	}
}
