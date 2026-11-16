// Command gen fetches the model catalog sources and writes the snapshot
// embedded in the binary:
//
//	go generate ./features/modelcatalog
//
// Only the fields Crewbase uses are kept (see modelcatalog.Entry).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hedeqiang/crewbase/features/modelcatalog"
)

func main() {
	out := flag.String("o", "snapshot.json.gz", "output file")
	empty := flag.Bool("empty", false, "write an empty snapshot (bootstrap)")
	flag.Parse()
	snap := modelcatalog.Snapshot{V: 1, Sources: []modelcatalog.SourceData{}}
	if !*empty {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		client := &http.Client{}
		for _, u := range modelcatalog.DefaultSources {
			d, err := modelcatalog.Fetch(ctx, client, u)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", u, err)
				os.Exit(1)
			}
			fmt.Fprintf(os.Stderr, "%s: %s, %d models\n", u, d.Format, len(d.Models))
			snap.Sources = append(snap.Sources, d)
		}
	}
	b, err := modelcatalog.EncodeGzip(snap)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", *out, len(b))
}
