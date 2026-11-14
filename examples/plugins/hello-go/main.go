// Command hello-go is an example Crewbase process plugin using the Go SDK.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	crewplugin "github.com/hedeqiang/crewbase/sdk/go"
)

func main() {
	p := crewplugin.New("hello-go", "0.1.0")
	p.Tool(crewplugin.Tool{
		Name:        "go_hello",
		Description: "Greet someone from a Go plugin.",
		Params: map[string]any{"type": "object",
			"properties": map[string]any{"name": map[string]any{"type": "string"}}},
		Execute: func(ctx context.Context, call crewplugin.Call, h *crewplugin.Host) (crewplugin.Result, error) {
			var a struct{ Name string }
			if err := call.Decode(&a); err != nil {
				return crewplugin.Result{}, err
			}
			if a.Name == "" {
				a.Name = "world"
			}
			h.Log("info", "greeting "+a.Name)
			return crewplugin.Text(fmt.Sprintf("Hello, %s! (from go, pid %d)", a.Name, os.Getpid())), nil
		},
	})
	if err := p.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
