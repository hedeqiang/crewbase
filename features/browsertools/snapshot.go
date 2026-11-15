package browsertools

import (
	"fmt"
	"strings"
)

// snapshotJS builds a compact text outline of the page. Interactive elements
// get numbers [n] (kept in window.__crewbaseRefs for click / type) and
// visible iframes are kept in window.__crewbaseFrames, so the caller can
// snapshot each frame (same- or cross-origin) and splice it in; the
// values of password, one-time-code and card fields are never read.
const snapshotJS = `(() => {
  const refs = [], lines = [], marks = [], frames = [];
  const hints = {password: false, otp: false, captcha: false};
  let seen = 0, buf = [];
  const clean = s => (s || '').replace(/\s+/g, ' ').trim();
  const cut = (s, n) => s.length > n ? s.slice(0, n) + '…' : s;
  const q = s => JSON.stringify(s);
  const flush = () => { const t = clean(buf.join(' ')); buf = []; if (t) { lines.push(cut(t, 300)); marks.push(0); } };
  const BLOCK = /^(ADDRESS|ARTICLE|ASIDE|BLOCKQUOTE|BR|DD|DIV|DL|DT|FIELDSET|FIGCAPTION|FIGURE|FOOTER|FORM|HEADER|HR|LI|MAIN|NAV|OL|P|PRE|SECTION|TABLE|TR|TD|TH|UL|DETAILS|DIALOG|LABEL)$/;
  const SKIP = /^(SCRIPT|STYLE|NOSCRIPT|TEMPLATE|SVG|CANVAS|HEAD|META|LINK|OBJECT|EMBED)$/;
  const ROLES = /^(button|link|checkbox|radio|tab|menuitem|menuitemcheckbox|switch|option|combobox|textbox|searchbox)$/;
  const visible = el => {
    const st = getComputedStyle(el);
    if (st.display === 'none' || st.visibility === 'hidden' || st.visibility === 'collapse') return false;
    if (el.getAttribute('aria-hidden') === 'true') return false;
    if (st.display === 'contents') return true;
    const r = el.getBoundingClientRect();
    return r.width > 0 || r.height > 0;
  };
  const sensitive = el => {
    const t = (el.getAttribute('type') || '').toLowerCase();
    const ac = (el.getAttribute('autocomplete') || '').toLowerCase();
    return t === 'password' || /password|one-time-code|cc-/.test(ac);
  };
  const label = el => {
    let s = el.getAttribute('aria-label') || '';
    if (!s && el.labels && el.labels.length) s = el.labels[0].innerText;
    if (!s) s = el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('alt') || el.getAttribute('name') || '';
    return cut(clean(s), 80);
  };
  const interactive = el => {
    const tag = el.tagName;
    if (tag === 'A') return el.hasAttribute('href');
    if (/^(BUTTON|SELECT|TEXTAREA|SUMMARY)$/.test(tag)) return true;
    if (tag === 'INPUT') return (el.type || '').toLowerCase() !== 'hidden';
    if (ROLES.test(el.getAttribute('role') || '')) return true;
    if (el.isContentEditable && !(el.parentElement && el.parentElement.isContentEditable)) return true;
    return el.hasAttribute('onclick');
  };
  const describe = (el, n) => {
    const tag = el.tagName.toLowerCase(), role = el.getAttribute('role') || '';
    let s;
    if (tag === 'a') {
      s = '[' + n + '] link ' + q(cut(clean(el.innerText) || label(el), 120));
      if (el.href && !/^javascript:/i.test(el.href)) s += ' -> ' + cut(el.href, 150);
    } else if (tag === 'input') {
      const t = (el.type || 'text').toLowerCase();
      if (/^(submit|button|reset|image)$/.test(t)) {
        s = '[' + n + '] button ' + q(cut(clean(el.value) || label(el), 80));
      } else if (t === 'checkbox' || t === 'radio') {
        s = '[' + n + '] ' + t + ' ' + q(label(el)) + (el.checked ? ' (checked)' : '');
      } else {
        s = '[' + n + '] input[' + t + '] ' + q(label(el));
        if (sensitive(el)) { if (el.value) s += ' (filled)'; }
        else if (el.value) s += ' value=' + q(cut(el.value, 80));
      }
    } else if (tag === 'textarea') {
      s = '[' + n + '] textarea ' + q(label(el)) + (el.value ? ' value=' + q(cut(el.value, 120)) : '');
    } else if (tag === 'select') {
      const opts = Array.from(el.options).slice(0, 12).map(o => clean(o.text)).join(' | ');
      const cur = el.selectedOptions && el.selectedOptions[0] ? clean(el.selectedOptions[0].text) : '';
      s = '[' + n + '] select ' + q(label(el)) + ' selected=' + q(cut(cur, 60)) + ' options: ' + cut(opts, 200);
    } else if (el.isContentEditable && !role) {
      s = '[' + n + '] editable ' + q(label(el) || cut(clean(el.innerText), 80));
    } else {
      s = '[' + n + '] ' + (role || 'button') + ' ' + q(cut(clean(el.innerText) || label(el), 100));
    }
    if (el.disabled || el.getAttribute('aria-disabled') === 'true') s += ' (disabled)';
    return s;
  };
  const walk = node => {
    if (seen++ > 8000) return;
    if (node.nodeType === 3) { buf.push(node.nodeValue); return; }
    if (node.nodeType !== 1) return;
    const el = node;
    if (SKIP.test(el.tagName)) return;
    if (el.tagName === 'IFRAME' || el.tagName === 'FRAME') {
      const src = el.getAttribute('src') || '';
      if (/captcha|recaptcha|hcaptcha|turnstile|challenges\.cloudflare|arkoselabs|funcaptcha/i.test(src + ' ' + (el.title || ''))) hints.captcha = true;
      if (visible(el)) { flush(); frames.push(el); lines.push('[iframe' + (el.title ? ' ' + q(cut(clean(el.title), 60)) : '') + ']'); marks.push(-frames.length); }
      return;
    }
    if (el.tagName === 'INPUT' && sensitive(el)) {
      if ((el.type || '').toLowerCase() === 'password') hints.password = true;
      if (/one-time-code/i.test(el.getAttribute('autocomplete') || '')) hints.otp = true;
    }
    if (!visible(el)) return;
    if (interactive(el)) { flush(); refs.push(el); lines.push(describe(el, refs.length)); marks.push(refs.length); return; }
    const m = /^H([1-6])$/.exec(el.tagName);
    if (m) { flush(); const t = clean(el.innerText); if (t) { lines.push('#'.repeat(+m[1]) + ' ' + cut(t, 200)); marks.push(0); } return; }
    const block = BLOCK.test(el.tagName);
    if (block) flush();
    if (el.shadowRoot) for (const c of el.shadowRoot.childNodes) walk(c);
    for (const c of el.childNodes) walk(c);
    if (block) flush();
  };
  walk(document.body || document.documentElement);
  flush();
  window.__crewbaseRefs = refs;
  window.__crewbaseFrames = frames;
  const text = (document.title + ' ' + (document.body ? document.body.innerText : '')).slice(0, 20000);
  if (/captcha|i'?m not a robot|verify (that )?you are (a )?human|are you a robot|human verification|人机验证|滑块验证|安全验证/i.test(text)) hints.captcha = true;
  if (/two-factor|two-step|\b2fa\b|verification code|authentication code|one-time (pass)?code|security code|verify (your identity|it'?s you)|验证码|动态码|二次验证|两步验证/i.test(text)) hints.otp = true;
  return {url: location.href, title: document.title, lines: lines, marks: marks, refs: refs.length, hints: hints};
})()`

// prepareClickJS scrolls ref n into view and returns its centre, or clicks it
// from script when it cannot be hit with the mouse.
const prepareClickJS = `((n, center) => {
  const el = (window.__crewbaseRefs || [])[n - 1];
  if (!el || !el.isConnected) return {error: 'stale'};
  if (el.tagName === 'A' && el.target && el.target !== '_self') el.removeAttribute('target');
  el.scrollIntoView(center ? {block: 'center', inline: 'center'} : {block: 'nearest', inline: 'nearest'});
  const r = el.getBoundingClientRect();
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  const hit = document.elementFromPoint(x, y);
  if (r.width === 0 || r.height === 0 || !hit || !(hit === el || el.contains(hit) || hit.contains(el) || (el.labels && Array.from(el.labels).some(l => l.contains(hit))))) {
    el.click();
    return {clicked: true};
  }
  return {x: x, y: y};
})(%d, %t)`

// prepareTypeJS checks ref n is a text field that is safe to type into and
// focuses it with its content selected (so typing replaces it).
const prepareTypeJS = `((n, center) => {
  const el = (window.__crewbaseRefs || [])[n - 1];
  if (!el || !el.isConnected) return {error: 'stale'};
  const tag = el.tagName, t = (el.getAttribute('type') || 'text').toLowerCase();
  const ac = (el.getAttribute('autocomplete') || '').toLowerCase();
  const id = ((el.getAttribute('name') || '') + ' ' + (el.id || '')).toLowerCase();
  if (t === 'password' || /password/.test(ac) || /passw|passwd|\bpwd\b/.test(id)) return {sensitive: 'password'};
  if (/one-time-code/.test(ac) || /\botp\b|totp|2fa|mfa/.test(id)) return {sensitive: 'one-time code'};
  if (/^cc-/.test(ac) || /\bcvv\b|\bcvc\b|card.?number/.test(id)) return {sensitive: 'payment card'};
  const textual = (tag === 'INPUT' && !/^(submit|button|reset|image|checkbox|radio|file|hidden|range|color)$/.test(t)) || tag === 'TEXTAREA';
  if (!textual && !el.isContentEditable) return {error: 'not a text field'};
  if (el.disabled || el.readOnly) return {error: 'the field is disabled or read-only'};
  el.scrollIntoView(center ? {block: 'center'} : {block: 'nearest'});
  el.focus();
  if (el.isContentEditable && !textual) {
    const range = document.createRange(); range.selectNodeContents(el);
    const sel = getSelection(); sel.removeAllRanges(); sel.addRange(range);
  } else {
    try { el.select(); } catch (e) { el.value = ''; }
  }
  return {ok: true};
})(%d, %t)`

const readyJS = `document.readyState`

// framesJS resolves after two animation frames (at most 300 ms).
const framesJS = `new Promise(r => { const t = setTimeout(r, 300); requestAnimationFrame(() => requestAnimationFrame(() => { clearTimeout(t); r(); })); })`

// frameOffsetJS returns where the content of iframe k (as kept by
// snapshotJS) starts in the frame's viewport, and the frame's viewport size.
const frameOffsetJS = `((k) => {
  const el = (window.__crewbaseFrames || [])[k];
  if (!el || !el.isConnected) return {error: 'stale'};
  const r = el.getBoundingClientRect(), st = getComputedStyle(el);
  return {x: r.left + el.clientLeft + (parseFloat(st.paddingLeft) || 0),
          y: r.top + el.clientTop + (parseFloat(st.paddingTop) || 0),
          w: innerWidth, h: innerHeight};
})(%d)`

// revealFrameJS scrolls iframe k (as kept by snapshotJS) into view.
const revealFrameJS = `((k) => {
  const el = (window.__crewbaseFrames || [])[k];
  if (!el || !el.isConnected) return {error: 'stale'};
  el.scrollIntoView({block: 'center', inline: 'nearest'});
  return {};
})(%d)`

// armClickJS makes ref n note the next trusted click that reaches it (or
// its children), so a mouse click into an iframe can be confirmed.
const armClickJS = `((n) => {
  const el = (window.__crewbaseRefs || [])[n - 1];
  if (!el || !el.isConnected) return {error: 'stale'};
  el.__crewbaseHit = false;
  if (!el.__crewbaseArmed) {
    el.__crewbaseArmed = true;
    el.addEventListener('click', (e) => { if (e.isTrusted) el.__crewbaseHit = true; }, true);
  }
  return {};
})(%d)`

// hitClickJS reports whether the armed ref n got the mouse click.
const hitClickJS = `((n) => {
  const el = (window.__crewbaseRefs || [])[n - 1];
  return {hit: !!(el && el.__crewbaseHit)};
})(%d)`

// scriptClickJS clicks ref n from script (when the mouse cannot reach it).
const scriptClickJS = `((n) => {
  const el = (window.__crewbaseRefs || [])[n - 1];
  if (!el || !el.isConnected) return {error: 'stale'};
  el.click();
  return {clicked: true};
})(%d)`

// snapshot is the decoded result of snapshotJS.
type snapshot struct {
	URL   string   `json:"url"`
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	// Marks[i] is the frame-local ref of Lines[i] (> 0), -k for the k-th
	// iframe (window.__crewbaseFrames[k-1]), 0 for text.
	Marks []int `json:"marks"`
	Refs  int   `json:"refs"`
	Hints struct {
		Password bool `json:"password"`
		OTP      bool `json:"otp"`
		Captcha  bool `json:"captcha"`
	} `json:"hints"`
}

// render formats a snapshot for the model, capped at max characters.
func (s snapshot) render(max int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Page: %s\nURL: %s\n", s.Title, s.URL)
	if h := s.hint(); h != "" {
		b.WriteString("\nHint: " + h + "\n")
	}
	fmt.Fprintf(&b, "\n%d interactive elements; use their [n] with browser_click / browser_type.\n\n", s.Refs)
	head := b.Len()
	truncated := 0
	for i, l := range s.Lines {
		if b.Len()-head+len(l)+1 > max {
			truncated = len(s.Lines) - i
			break
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "… [%d more lines not shown]\n", truncated)
	}
	if len(s.Lines) == 0 {
		b.WriteString("(the page has no visible content)\n")
	}
	return b.String()
}

func (s snapshot) hint() string {
	var kinds []string
	if s.Hints.Captcha {
		kinds = append(kinds, "a captcha / human check")
	}
	if s.Hints.OTP {
		kinds = append(kinds, "a two-factor / verification code step")
	}
	if s.Hints.Password {
		kinds = append(kinds, "a login form (password field)")
	}
	if len(kinds) == 0 {
		return ""
	}
	return "this page looks like " + strings.Join(kinds, " and ") +
		". Do not type passwords or codes yourself: call browser_request_takeover with a short reason so the user can complete it, then continue."
}
