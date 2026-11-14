// Command crew-relay is the Crewbase relay server: hosts (crewd) and their
// paired devices both connect out to it, so no host opens an inbound port.
// It forwards end-to-end encrypted frames and never sees tokens or
// content. With the web UI built in, it also serves the app devices open
// from a pairing link.
//
//	crew-relay --addr :8443 --tls-cert cert.pem --tls-key key.pem
//	crew-relay --addr 127.0.0.1:8080          # behind a TLS-terminating proxy
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hedeqiang/crewbase/apps/web"
	"github.com/hedeqiang/crewbase/features/relay"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	cert := flag.String("tls-cert", "", "TLS certificate (PEM); omit behind a TLS proxy")
	key := flag.String("tls-key", "", "TLS key (PEM)")
	origins := flag.String("origins", "", "extra allowed web origins for device connections, comma-separated hosts")
	maxHosts := flag.Int("max-hosts", 1000, "hosts online at once")
	noWeb := flag.Bool("no-web", false, "do not serve the web app")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := relay.ServerConfig{MaxHosts: *maxHosts, Log: log}
	if *origins != "" {
		cfg.Origins = strings.Split(*origins, ",")
	}
	if web.Available() && !*noWeb {
		cfg.Web = web.Dist
	}
	srv := &http.Server{Addr: *addr, Handler: relay.NewServer(cfg).Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		var err error
		if *cert != "" {
			err = srv.ListenAndServeTLS(*cert, *key)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}()
	log.Info("relay listening", "addr", *addr, "tls", *cert != "", "web", cfg.Web != nil)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
