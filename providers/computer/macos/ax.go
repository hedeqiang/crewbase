package macos

import (
	"time"

	"github.com/hedeqiang/crewbase/capabilities/computer"
)

// Electron and Chromium apps (VS Code, Feishu / Lark, Slack, Discord,
// Notion, Obsidian…) build their accessibility tree only once something
// asks for it. AXManualAccessibility on the application element is
// Electron's switch; AXEnhancedUserInterface is the older Chromium one,
// which also changes window animations, so it is only the fallback.
const (
	attrManualAX   = "AXManualAccessibility"
	attrEnhancedUI = "AXEnhancedUserInterface"

	// sparseElems: an outline with fewer numbered elements than this…
	sparseElems = 5
	// …of a window at least this big (points) is "nearly empty".
	sparseMinW, sparseMinH = 400, 300

	axWait = 1500 * time.Millisecond // how long the tree may take to build
	axPoll = 250 * time.Millisecond
)

// sparse reports whether an outline with n numbered elements of a window
// with frame is nearly empty: what an Electron app shows before its
// accessibility is switched on.
func sparse(n int, frame computer.Rect) bool {
	return n < sparseElems && frame.W >= sparseMinW && frame.H >= sparseMinH
}

// claimManualAX reports whether accessibility should be switched on for
// pid now: its outline is sparse and it was never tried for pid. The claim
// is recorded, so each process is tried once whatever the outcome.
func (p *Provider) claimManualAX(pid, n int, frame computer.Rect) bool {
	if pid <= 0 || !sparse(n, frame) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.axTried[pid] {
		return false
	}
	p.axTried[pid] = true
	return true
}

// enableAX switches an app's accessibility tree on: AXManualAccessibility,
// else AXEnhancedUserInterface. It returns the attribute that took ("" =
// neither).
func (p *Provider) enableAX(appEl ref) string {
	if err := p.pf.SetBool(appEl, attrManualAX, true); err == nil {
		return attrManualAX
	}
	if err := p.pf.SetBool(appEl, attrEnhancedUI, true); err == nil {
		return attrEnhancedUI
	}
	return ""
}
