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
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/hedeqiang/crewbase/apps/launch"
	"github.com/hedeqiang/crewbase/kernel"
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
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "crew:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: crew plugins|agents|agent new|chat|approvals|approve|reject|dump-config|daemon")
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
	return call("POST", "/api/approvals/"+args[0], body, nil)
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
