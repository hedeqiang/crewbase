// Package vaultapi is Settings › 保管库 on the gateway: the secrets in the
// vault (capabilities/credentials, vault.go) by name and what they are for,
// masked. Values are write-only: they can be added, replaced and deleted,
// never read back.
//
//	GET    /api/vault                 {items: [{id, name, masked, kind, target, created, updated}]}
//	POST   /api/vault  {name, value}  adds crewbase/<name> (a secret to refer to as vault:crewbase/<name>)
//	PUT    /api/vault  {id, value}    replaces an item's value
//	DELETE /api/vault?id=<id>         deletes an item
package vaultapi

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/hedeqiang/crewbase/capabilities/credentials"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Item is one vault entry as listed.
type Item struct {
	ID     string `json:"id"`
	Ref    string `json:"ref"` // vault:<id>, to paste into a config
	Masked string `json:"masked"`
	// Kind and Target say what the secret is for (from its name):
	// provider / model (a model provider's API key), mcp (an MCP server's
	// token or variable), mcp-login (an MCP server's sign-in), im (an IM
	// bot), secret (one the person added).
	Kind    string    `json:"kind"`
	Target  string    `json:"target,omitempty"`
	Key     string    `json:"key,omitempty"` // the variable, for mcp
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// kinds that model provider adapters use for their default key
// ("vault:crewbase/<provider id>").
var modelIDs = map[string]bool{"anthropic": true, "openai": true, "gemini": true, "deepseek": true}

// Describe works out what a vault id is for.
func Describe(id string) Item {
	it := Item{ID: id, Ref: "vault:" + id, Kind: "secret"}
	svc, name, ok := strings.Cut(id, "/")
	if !ok || svc != "crewbase" {
		return it
	}
	switch {
	case strings.HasPrefix(name, "provider-"):
		it.Kind, it.Target = "provider", strings.TrimPrefix(name, "provider-")
	case strings.HasPrefix(name, "mcp-oauth-"):
		it.Kind, it.Target = "mcp-login", strings.TrimPrefix(name, "mcp-oauth-")
	case strings.HasPrefix(name, "mcp-"):
		rest := strings.TrimPrefix(name, "mcp-")
		// mcp-<server>-<key>; the server name may itself contain "-"
		if i := strings.LastIndex(rest, "-"); i > 0 {
			it.Kind, it.Target, it.Key = "mcp", rest[:i], rest[i+1:]
		} else {
			it.Kind, it.Target = "mcp", rest
		}
	case strings.HasPrefix(name, "im-"):
		rest := strings.TrimPrefix(name, "im-")
		rest = strings.TrimSuffix(strings.TrimSuffix(rest, "-bot"), "-app")
		it.Kind, it.Target = "im", rest
	case strings.HasSuffix(name, "-bot") || strings.HasSuffix(name, "-app"):
		it.Kind, it.Target = "im", strings.TrimSuffix(strings.TrimSuffix(name, "-bot"), "-app")
	case modelIDs[name]:
		it.Kind, it.Target = "model", name
	default:
		it.Target = name
	}
	return it
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// Plugin registers the vault routes (desktop only).
var Plugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/vault-api",
	Inject:      []string{gateway.ServiceKey, credentials.ServiceKey},
	Description: "Settings › 保管库: the vault's secrets by name, masked; add, replace, delete (never read back).",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		cs := kernel.Use[*credentials.Service](c, credentials.ServiceKey)
		vault := func() (*credentials.Vault, error) {
			v := cs.Vault()
			if v == nil {
				return nil, gateway.BadRequest("this Crewbase has no vault (no data folder)")
			}
			return v, nil
		}
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/vault",
			Handle: func(context.Context, *gateway.Request) (any, error) {
				v, err := vault()
				if err != nil {
					return nil, err
				}
				list, err := v.List()
				if err != nil {
					return nil, err
				}
				items := make([]Item, 0, len(list))
				for _, x := range list {
					it := Describe(x.ID)
					it.Masked, it.Created, it.Updated = x.Masked, x.Created, x.Updated
					items = append(items, it)
				}
				return map[string]any{"items": items}, nil
			}})
		type body struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/vault",
			Validate: func(r *gateway.Request) error {
				var b body
				if err := r.Decode(&b); err != nil {
					return err
				}
				if !nameRe.MatchString(b.Name) {
					return gateway.BadRequest("a name is letters, digits and . _ @ - (at most 64)")
				}
				if strings.TrimSpace(b.Value) == "" {
					return gateway.BadRequest("value is required")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b body
				_ = r.Decode(&b)
				v, err := vault()
				if err != nil {
					return nil, err
				}
				id := "crewbase/" + b.Name
				if list, err := v.List(); err == nil {
					for _, x := range list {
						if x.ID == id {
							return nil, gateway.Conflict("a secret named %s already exists; replace it instead", b.Name)
						}
					}
				}
				if err := cs.Set("vault:"+id, strings.TrimSpace(b.Value)); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				it := Describe(id)
				it.Masked = credentials.Mask(strings.TrimSpace(b.Value))
				return it, nil
			}})
		g.Handle(c, gateway.Route{Method: "PUT", Path: "/api/vault",
			Validate: func(r *gateway.Request) error {
				var b body
				if err := r.Decode(&b); err != nil {
					return err
				}
				if b.ID == "" || strings.TrimSpace(b.Value) == "" {
					return gateway.BadRequest("id and value are required")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				var b body
				_ = r.Decode(&b)
				if !cs.Describe("vault:" + b.ID).Configured {
					return nil, gateway.NotFound()
				}
				if err := cs.Set("vault:"+b.ID, strings.TrimSpace(b.Value)); err != nil {
					return nil, gateway.BadRequest("%v", err)
				}
				it := Describe(b.ID)
				it.Masked = credentials.Mask(strings.TrimSpace(b.Value))
				return it, nil
			}})
		g.Handle(c, gateway.Route{Method: "DELETE", Path: "/api/vault",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				id := r.HTTP.URL.Query().Get("id")
				if id == "" || !cs.Describe("vault:"+id).Configured {
					return nil, gateway.NotFound()
				}
				return nil, cs.Delete("vault:" + id)
			}})
		return nil
	},
})
