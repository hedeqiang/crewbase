package chrome

// Render: a page in a throwaway headless Chrome (a fresh temporary
// profile, deleted after), for an agent to check a page it made. It
// shares nothing with the agents' own browsers.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/hedeqiang/crewbase/capabilities/browser"
)

const maxRenderNotes = 20

// Render implements browser.Renderer.
func (c *Chrome) Render(ctx context.Context, url string, o browser.RenderOptions) (browser.Rendered, error) {
	if c.exe == "" {
		return browser.Rendered{}, errors.New("no local Chrome to render with (the browser is attached)")
	}
	if len(o.Sizes) == 0 {
		o.Sizes = []browser.Size{{Width: 1280, Height: 800}}
	}
	if o.Settle <= 0 {
		o.Settle = 800 * time.Millisecond
	}
	dir, err := os.MkdirTemp("", "crewbase-render-")
	if err != nil {
		return browser.Rendered{}, err
	}
	defer os.RemoveAll(dir)
	opts := append(chromedp.DefaultExecAllocatorOptions[:0:0], chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(c.exe), chromedp.UserDataDir(dir), chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("mute-audio", true), chromedp.WindowSize(o.Sizes[0].Width, o.Sizes[0].Height))
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	actx, cancelA := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelA()
	tctx, cancelT := chromedp.NewContext(actx)
	defer cancelT()

	var mu sync.Mutex
	var out browser.Rendered
	note := func(list *[]string, s string) {
		mu.Lock()
		defer mu.Unlock()
		s = strings.TrimSpace(s)
		if s == "" || len(*list) >= maxRenderNotes {
			return
		}
		for _, x := range *list {
			if x == s {
				return
			}
		}
		*list = append(*list, s)
	}
	reqs := map[network.RequestID]string{}
	listen := func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventExceptionThrown:
			d := e.ExceptionDetails
			msg := d.Text
			if d.Exception != nil && d.Exception.Description != "" {
				msg = strings.SplitN(d.Exception.Description, "\n", 2)[0]
			}
			if d.LineNumber > 0 {
				msg += fmt.Sprintf(" (line %d)", d.LineNumber+1)
			}
			note(&out.Errors, msg)
		case *runtime.EventConsoleAPICalled:
			if e.Type != runtime.ConsoleAPICalledTypeError && e.Type != runtime.ConsoleAPICalledTypeAssert {
				return
			}
			var parts []string
			for _, a := range e.Args {
				if a.Description != "" {
					parts = append(parts, a.Description)
				} else if len(a.Value) > 0 {
					parts = append(parts, strings.Trim(string(a.Value), `"`))
				}
			}
			note(&out.Errors, "console.error: "+strings.Join(parts, " "))
		case *network.EventRequestWillBeSent:
			mu.Lock()
			reqs[e.RequestID] = e.Request.URL
			mu.Unlock()
		case *network.EventLoadingFailed:
			if e.Canceled {
				return
			}
			mu.Lock()
			u := reqs[e.RequestID]
			mu.Unlock()
			if strings.HasSuffix(u, "/favicon.ico") {
				return
			}
			note(&out.Failed, u+": "+e.ErrorText)
		case *network.EventResponseReceived:
			// the browser asks for /favicon.ico by itself; a page without one is fine
			if e.Response.Status >= 400 && !strings.HasSuffix(e.Response.URL, "/favicon.ico") {
				note(&out.Failed, fmt.Sprintf("%s: HTTP %d", e.Response.URL, e.Response.Status))
			}
		}
	}
	if err := chromedp.Listen(tctx, listen); err != nil {
		return out, fmt.Errorf("render: start Chrome: %w", err)
	}

	metrics := func(sz browser.Size) error {
		_, err := chromedp.Call(tctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{
			Width: int64(sz.Width), Height: int64(sz.Height), DeviceScaleFactor: 1, Mobile: sz.Width < 600})
		return err
	}
	if _, err := chromedp.Call(tctx, network.Enable, network.EnableParams{}); err != nil {
		return out, fmt.Errorf("render: %w", err)
	}
	if _, err := chromedp.Call(tctx, runtime.Enable, cdp.Empty{}); err != nil {
		return out, fmt.Errorf("render: %w", err)
	}
	if err := metrics(o.Sizes[0]); err != nil {
		return out, fmt.Errorf("render: %w", err)
	}
	if err := chromedp.Do(tctx, chromedp.Navigate(url), chromedp.Sleep(o.Settle)); err != nil {
		return out, fmt.Errorf("render %s: %w", url, err)
	}
	out.Title, _ = chromedp.Run(tctx, chromedp.Title())
	for i, sz := range o.Sizes {
		if i > 0 {
			if err := metrics(sz); err != nil {
				return out, err
			}
			_ = chromedp.Do(tctx, chromedp.Sleep(300*time.Millisecond))
		}
		shot := browser.Shot{Size: sz}
		type measure struct {
			Text     int `json:"text"`
			Elements int `json:"elements"`
			Overflow int `json:"overflow"`
			Height   int `json:"height"`
		}
		if m, err := chromedp.Run(tctx, chromedp.Evaluate[measure](measureJS)); err == nil {
			shot.TextChars, shot.Elements, shot.Overflow, shot.PageHeight = m.Text, m.Elements, m.Overflow, m.Height
		}
		png, err := chromedp.Run(tctx, chromedp.CaptureScreenshot())
		if err != nil {
			return out, err
		}
		shot.PNG = png
		out.Shots = append(out.Shots, shot)
	}
	return out, nil
}

// measureJS: visible text, visible elements, horizontal overflow, height.
const measureJS = `(() => {
  const d = document.documentElement, b = document.body;
  let elements = 0;
  for (const el of document.querySelectorAll('body *')) {
    const r = el.getBoundingClientRect();
    if (r.width > 1 && r.height > 1) elements++;
  }
  return {
    text: b ? (b.innerText || '').trim().length : 0,
    elements,
    overflow: Math.max(0, Math.max(d.scrollWidth, b ? b.scrollWidth : 0) - d.clientWidth),
    height: Math.max(d.scrollHeight, b ? b.scrollHeight : 0),
  };
})()`

var _ browser.Renderer = (*Chrome)(nil)
