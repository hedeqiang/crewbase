package im

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Gateways lists the running IM gateways, sorted by name.
func Gateways(c *kernel.Context) []*Gateway {
	var out []*Gateway
	for _, k := range c.Host().Services() {
		if !strings.HasPrefix(k, ServicePrefix) {
			continue
		}
		if g, ok := kernel.Lookup[*Gateway](c, k); ok {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// APIPlugin exposes gateway status and a test message. Tokens are never
// returned (Status has no token fields).
//
//	GET  /api/im              → {"gateways": [Status…]}
//	POST /api/im/{name}/test  → {"ok": true, "ref": {...}} or {"ok": false, "error": "..."}
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/im-api",
	Inject:      []string{gateway.ServiceKey},
	Description: "IM gateway status and test messages (/api/im).",
	Apply: func(c *kernel.Context) error {
		gw := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		gw.Handle(c, gateway.Route{Method: "GET", Path: "/api/im",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				list := []Status{}
				for _, g := range Gateways(c) {
					list = append(list, g.Status())
				}
				return map[string]any{"gateways": list}, nil
			}})
		gw.Handle(c, gateway.Route{Method: "POST", Path: "/api/im/{name}/test",
			Handle: func(ctx context.Context, r *gateway.Request) (any, error) {
				g, ok := kernel.Lookup[*Gateway](c, ServicePrefix+r.Params["name"])
				if !ok {
					return nil, gateway.NotFound()
				}
				cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				ref, err := g.Test(cctx)
				if err != nil {
					// A failed send is a result, not an API error.
					return map[string]any{"ok": false, "error": g.Scrub(err.Error())}, nil
				}
				return map[string]any{"ok": true, "ref": ref}, nil
			}})
		return nil
	},
})
