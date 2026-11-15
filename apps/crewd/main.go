// Command crewd is the Crewbase daemon: the plugin host plus every builtin
// plugin, the local API gateway and (when built with the web UI) the
// desktop interface.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hedeqiang/crewbase/apps/launch"
	"github.com/hedeqiang/crewbase/apps/web"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/internal/loginpath"
	"github.com/hedeqiang/crewbase/kernel"
)

type multi []string

func (m *multi) String() string     { return fmt.Sprint(*m) }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var o launch.Options
	var patches multi
	dump := flag.Bool("dump-config", false, "print the resolved plugin tree and exit")
	flag.StringVar(&o.Profile, "profile", "desktop", "profile: desktop | server | sdk")
	flag.StringVar(&o.Root, "root", "", "data directory (default ~/.crewbase)")
	flag.StringVar(&o.Addr, "addr", "", "gateway listen address (default 127.0.0.1:7788)")
	flag.Var(&patches, "patch", "extra patch file (repeatable)")
	flag.Parse()
	o.Patches = patches
	// started by launchd, systemd or a GUI, PATH may lack where the agent
	// CLIs live; the login shell's PATH is added (a terminal's is kept)
	loginpath.Fix()

	if *dump {
		entries, err := launch.Resolve(o)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Print(kernel.DumpConfig(entries))
		return
	}
	if web.Available() {
		gateway.Static = web.Dist
	}
	h, err := launch.Start(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, p := range h.Plugins() {
		if p.State != kernel.StateActive {
			fmt.Fprintf(os.Stderr, "plugin %s: %s %v %s\n", p.ID, p.State, p.Missing, p.Error)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	// Close unloads every plugin in reverse order: subprocess trees are
	// killed, sessions flushed.
	h.Close()
}
