package main

import (
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/hedeqiang/crewbase/core/agent"
	"github.com/hedeqiang/crewbase/features/computertools"
	"github.com/hedeqiang/crewbase/kernel"
)

// Computer use in the shell (docs/computer-use.md §6): while an agent
// operates an app,
//   - the menu bar says who operates which app, with 停止电脑操作;
//   - a click-through border in the agent's colour frames the target
//     window;
//   - ⌃⌥⌘. stops every agent's computer use at once (any app in front).
// The state comes from computertools.Changed (in-process host only).

// stopHotkey stops all computer use.
const stopHotkey = "Control+Alt+Command+."

// liveLinger keeps the indicator up briefly between an agent's calls, so
// it does not flicker off between two steps.
const liveLinger = 1500 * time.Millisecond

type computerUI struct {
	s *shell

	mu      sync.Mutex
	state   computertools.State
	shown   []computertools.Live // what the indicator shows (lingering)
	clearAt time.Time
	borders map[string]*mygo.Window // by agent
	timer   *time.Timer
}

// watchComputer follows the computer-use state of the in-process host.
func (s *shell) watchComputer(d *daemon) {
	if d.host == nil {
		return // attached to another daemon: it has no shell to draw in
	}
	u := &computerUI{s: s, borders: map[string]*mygo.Window{}}
	s.mu.Lock()
	s.cu = u
	s.mu.Unlock()
	computertools.Changed.Listen(d.host.Root(), func(st computertools.State) { u.update(st) })
	mygo.RunOnMain(func() {
		if err := mygo.GlobalShortcut.Register(stopHotkey, u.stopAll); err != nil {
			slog.Warn("desktop: computer-use stop hotkey", "err", err)
		}
	})
}

func (u *computerUI) service() *computertools.Service {
	d := u.s.daemon()
	if d == nil || d.host == nil {
		return nil
	}
	svc, _ := kernel.Lookup[*computertools.Service](d.host.Root(), computertools.ServiceKey)
	return svc
}

// stopAll is the hotkey and the menu item.
func (u *computerUI) stopAll() {
	if svc := u.service(); svc != nil {
		svc.StopAll()
		slog.Info("desktop: computer use stopped by the user")
	}
}

func (u *computerUI) update(st computertools.State) {
	u.mu.Lock()
	u.state = st
	if len(st.Live) > 0 {
		u.shown = append([]computertools.Live(nil), st.Live...)
		u.clearAt = time.Time{}
	} else if len(u.shown) > 0 {
		u.clearAt = time.Now().Add(liveLinger)
		if u.timer == nil {
			u.timer = time.AfterFunc(liveLinger, u.expire)
		} else {
			u.timer.Reset(liveLinger)
		}
	}
	u.mu.Unlock()
	u.redraw()
}

func (u *computerUI) expire() {
	u.mu.Lock()
	if !u.clearAt.IsZero() && time.Now().After(u.clearAt) {
		u.shown = nil
		u.clearAt = time.Time{}
	}
	u.mu.Unlock()
	u.redraw()
}

// live is what to show now (lingering included).
func (u *computerUI) live() []computertools.Live {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]computertools.Live(nil), u.shown...)
}

// Label is the menu bar line: "Kai 正在操作 备忘录".
func liveLabel(name string, l computertools.Live) string {
	app := l.AppName
	if app == "" {
		app = l.App
	}
	if l.Paused {
		return fmt.Sprintf(tr("cuPaused"), name, app)
	}
	return fmt.Sprintf(tr("cuLive"), name, app)
}

func (u *computerUI) names() map[string]agent.Spec {
	out := map[string]agent.Spec{}
	d := u.s.daemon()
	if d == nil || d.host == nil {
		return out
	}
	if reg, ok := kernel.Lookup[*agent.Registry](d.host.Root(), agent.ServiceKey); ok {
		for _, a := range reg.List() {
			out[a.ID()] = a.Spec()
		}
	}
	return out
}

// menuItems are the tray's computer-use lines (nil when nobody operates).
func (u *computerUI) menuItems() []*mygo.MenuItem {
	live := u.live()
	if len(live) == 0 {
		return nil
	}
	specs := u.names()
	var items []*mygo.MenuItem
	for _, l := range live {
		name := l.Agent
		if sp, ok := specs[l.Agent]; ok && sp.Name != "" {
			name = sp.Name
		}
		session := specs[l.Agent].Session
		items = append(items, &mygo.MenuItem{Label: "● " + liveLabel(name, l), Click: func(*mygo.MenuItem, *mygo.Window) { u.s.openSession(session) }})
	}
	items = append(items, &mygo.MenuItem{Label: tr("cuStop"), Accelerator: stopHotkey, Click: func(*mygo.MenuItem, *mygo.Window) { u.stopAll() }}, mygo.Separator())
	return items
}

// title is the tray title while someone operates an app (macOS).
func (u *computerUI) title() string {
	live := u.live()
	if len(live) == 0 {
		return ""
	}
	app := live[0].AppName
	if app == "" {
		app = live[0].App
	}
	if len(live) > 1 {
		app = fmt.Sprintf("%s +%d", app, len(live)-1)
	}
	return "● " + app
}

func (u *computerUI) redraw() {
	u.s.updateTray()
	mygo.RunOnMain(u.drawBorders)
}

// borderMargin draws the frame just outside the window.
const borderMargin = 4

// drawBorders keeps one click-through frame per operating agent. Main
// thread only.
func (u *computerUI) drawBorders() {
	if runtime.GOOS != "darwin" {
		return
	}
	live := u.live()
	specs := u.names()
	want := map[string]computertools.Live{}
	for _, l := range live {
		if l.Window != nil && l.Window.W > 0 && l.Window.H > 0 {
			want[l.Agent] = l
		}
	}
	for id, w := range u.borders {
		if _, ok := want[id]; !ok || w.IsDestroyed() {
			if !w.IsDestroyed() {
				w.Close()
			}
			delete(u.borders, id)
		}
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		l := want[id]
		r := mygo.Rectangle{
			X:      int(l.Window.X) - borderMargin,
			Y:      int(l.Window.Y) - borderMargin,
			Width:  int(l.Window.W) + 2*borderMargin,
			Height: int(l.Window.H) + 2*borderMargin,
		}
		color := specs[id].Color
		if color == "" {
			color = "#3987e5"
		}
		if w := u.borders[id]; w != nil {
			w.SetBounds(r)
			continue
		}
		w := newBorderWindow(r, color, l.Paused)
		if w != nil {
			u.borders[id] = w
		}
	}
}

// newBorderWindow opens a transparent, click-through panel with a
// coloured frame. It never takes focus or input.
func newBorderWindow(r mygo.Rectangle, color string, paused bool) *mygo.Window {
	style := "solid"
	if paused {
		style = "dashed"
	}
	html := `<!doctype html><meta charset="utf-8"><style>html,body{margin:0;height:100%;background:transparent;overflow:hidden}` +
		`div{position:fixed;inset:0;border:3px ` + style + ` ` + color + `;border-radius:12px;box-shadow:0 0 18px ` + color + `66, inset 0 0 12px ` + color + `33}</style><div></div>`
	w := mygo.NewWindow(mygo.WindowOptions{
		Title: "Crewbase computer use",
		X:     r.X, Y: r.Y, Width: r.Width, Height: r.Height,
		Frameless: true, Transparent: true, Panel: true, AlwaysOnTop: true,
		DisableResize: true, DisableMove: true, DisableShadow: true, SkipTaskbar: true,
	})
	if w == nil {
		slog.Warn("desktop: computer-use border window not created")
		return nil
	}
	w.SetIgnoreMouseEvents(true)
	w.Page().LoadHTML(strings.TrimSpace(html), "about:blank")
	w.ShowInactive()
	return w
}
