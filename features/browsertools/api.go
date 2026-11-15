package browsertools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image/png"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/hedeqiang/crewbase/capabilities/browser"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/gateway"
	"github.com/hedeqiang/crewbase/kernel"
)

// Caller identifies the person behind a gateway request without exposing
// the token: "<device>:<first 8 bytes of sha256(token)>".
func Caller(r *gateway.Request) string {
	tok := ""
	if r.HTTP != nil {
		if h := r.HTTP.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			tok = strings.TrimPrefix(h, "Bearer ")
		} else {
			tok = r.HTTP.URL.Query().Get("token")
		}
	}
	sum := sha256.Sum256([]byte(tok))
	return r.Device + ":" + hex.EncodeToString(sum[:8])
}

// Local reports whether the request comes from the desktop on the machine
// running Crewbase (the local token over loopback), where a browser window
// would be visible.
func Local(r *gateway.Request) bool {
	if r.Device != "desktop" || r.DeviceID != "" || r.HTTP == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.HTTP.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Screenshot is the takeover view frame returned by the API.
type Screenshot struct {
	Mime   string `json:"mime"`
	Data   string `json:"data"` // base64 PNG
	Width  int    `json:"width"`
	Height int    `json:"height"`
	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
}

func apiErr(err error) error {
	switch {
	case errors.Is(err, browser.ErrNotController):
		return &gateway.Error{Status: 403, Code: "forbidden", Message: "the browser is controlled by someone else"}
	case errors.Is(err, browser.ErrNotTakenOver):
		return gateway.Conflict("take over the browser first")
	case errors.Is(err, browser.ErrNotRunning):
		return gateway.Conflict("the browser is not running")
	case errors.Is(err, browser.ErrNoWindow):
		return gateway.Conflict("%v", err)
	case errors.Is(err, browser.ErrClosed):
		return gateway.Conflict("the browser provider is unavailable")
	}
	return gateway.BadRequest("%v", err)
}

// APIPlugin exposes the takeover flow to people. Screenshots and input are
// served only to the person who took over (screenshots also while the agent
// is not running) and are never sent to the model or logged.
var APIPlugin = kernel.Register(kernel.Spec{
	Name:        "@crewbase/browser-api",
	Inject:      []string{gateway.ServiceKey, browser.ServiceKey},
	Optional:    []string{agent.ServiceKey},
	Description: "Browser takeover API: state, takeover, handback, screenshot frames and input, move to a desktop window and back; browser.state SSE.",
	Apply: func(c *kernel.Context) error {
		g := kernel.Use[*gateway.Gateway](c, gateway.ServiceKey)
		b := kernel.Use[browser.Browser](c, browser.ServiceKey)
		agentFor := func(id string) (*agent.Agent, error) {
			reg, ok := kernel.Lookup[*agent.Registry](c, agent.ServiceKey)
			if !ok {
				return nil, nil
			}
			a, ok := reg.Get(id)
			if !ok {
				return nil, gateway.NotFound()
			}
			return a, nil
		}
		mobile := []string{"mobile", "im"}

		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/browser", Devices: mobile,
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				if _, err := agentFor(r.Params["id"]); err != nil {
					return nil, err
				}
				st := b.State(r.Params["id"])
				_, win := b.(browser.Windower)
				return struct {
					browser.State
					Mine bool `json:"mine"`
					// WindowHere: this client can move the takeover to a window
					// (it runs on the machine where the window would open).
					WindowHere bool `json:"windowHere"`
				}{st, st.TakenOverBy != "" && st.TakenOverBy == Caller(r), win && st.CanWindow && Local(r)}, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/browser/takeover",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				id := r.Params["id"]
				if _, err := agentFor(id); err != nil {
					return nil, err
				}
				if err := b.Takeover(id, Caller(r)); err != nil {
					return nil, apiErr(err)
				}
				return b.State(id), nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/browser/handback",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				id := r.Params["id"]
				if _, err := agentFor(id); err != nil {
					return nil, err
				}
				st := b.State(id)
				if st.TakenOverBy == "" {
					return nil, apiErr(browser.ErrNotTakenOver)
				}
				if st.TakenOverBy != Caller(r) {
					return nil, apiErr(browser.ErrNotController)
				}
				if err := b.Handback(id); err != nil {
					return nil, apiErr(err)
				}
				return b.State(id), nil
			}})
		// Move a takeover to a real window on the desktop and back. Only for
		// the local desktop: the window opens on the machine running
		// Crewbase, which a phone or a remote client cannot see.
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/browser/window",
			Validate: func(r *gateway.Request) error {
				var in struct {
					Open *bool `json:"open"`
				}
				if err := r.Decode(&in); err != nil {
					return err
				}
				if in.Open == nil {
					return gateway.BadRequest("open (true or false) is required")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				id := r.Params["id"]
				if _, err := agentFor(id); err != nil {
					return nil, err
				}
				w, ok := b.(browser.Windower)
				if !ok {
					return nil, gateway.Conflict("this browser cannot open a window")
				}
				var in struct {
					Open bool `json:"open"`
				}
				_ = r.Decode(&in)
				if in.Open && !Local(r) {
					return nil, &gateway.Error{Status: 403, Code: "forbidden", Message: "the window opens on the computer running Crewbase; open it from there"}
				}
				if err := w.SetWindow(id, Caller(r), in.Open); err != nil {
					return nil, apiErr(err)
				}
				return b.State(id), nil
			}})
		g.Handle(c, gateway.Route{Method: "GET", Path: "/api/agents/{id}/browser/screenshot",
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				id := r.Params["id"]
				a, err := agentFor(id)
				if err != nil {
					return nil, err
				}
				st := b.State(id)
				switch {
				case st.TakenOverBy != "" && st.TakenOverBy != Caller(r):
					return nil, apiErr(browser.ErrNotController)
				case st.TakenOverBy == "":
					// Without a takeover, only while the agent is not working.
					if a == nil {
						return nil, gateway.Conflict("take over the browser first")
					}
					if s, _ := a.Status(); s == agent.StatusRunning {
						return nil, gateway.Conflict("the agent is using the browser; take over first")
					}
				}
				img, err := b.Screenshot(id)
				if err != nil {
					return nil, apiErr(err)
				}
				out := Screenshot{Mime: "image/png", URL: st.URL, Title: st.Title}
				if cfg, err := png.DecodeConfig(bytes.NewReader(img)); err == nil {
					out.Width, out.Height = cfg.Width, cfg.Height
				}
				// ?raw=1: the PNG itself (a third smaller than base64 JSON);
				// size and URL travel in headers (title is not ASCII-safe).
				if r.HTTP.URL.Query().Get("raw") == "1" {
					return &gateway.Blob{Name: "screen.png", Mime: "image/png", Data: img, NoStore: true, Meta: map[string]string{
						"Width": strconv.Itoa(out.Width), "Height": strconv.Itoa(out.Height), "Url": url.PathEscape(st.URL),
					}}, nil
				}
				out.Data = base64.StdEncoding.EncodeToString(img)
				return out, nil
			}})
		g.Handle(c, gateway.Route{Method: "POST", Path: "/api/agents/{id}/browser/input",
			Validate: func(r *gateway.Request) error {
				var in browser.Input
				if err := r.Decode(&in); err != nil {
					return err
				}
				switch in.Type {
				case browser.InputClick, browser.InputScroll:
				case browser.InputKey:
					if in.Text == "" {
						return gateway.BadRequest("key input needs text")
					}
				case browser.InputNavigate:
					if in.URL == "" {
						return gateway.BadRequest("navigate input needs url")
					}
				default:
					return gateway.BadRequest("type must be click, key, navigate or scroll")
				}
				return nil
			},
			Handle: func(_ context.Context, r *gateway.Request) (any, error) {
				id := r.Params["id"]
				if _, err := agentFor(id); err != nil {
					return nil, err
				}
				var in browser.Input
				_ = r.Decode(&in)
				if err := b.Input(id, Caller(r), in); err != nil {
					return nil, apiErr(err)
				}
				return nil, nil
			}})

		browser.Changed.Listen(c, func(st browser.State) { g.Hub().Publish("browser.state", st) })
		return nil
	},
})
