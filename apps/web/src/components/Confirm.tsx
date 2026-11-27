import { useEffect, useRef, useState } from "react";
import { t } from "../i18n";
import { Popover } from "./Popover";

// what was last pressed: the question opens in place, below it
let pressed: { el: HTMLElement; at: number } | null = null;
if (typeof document !== "undefined")
  document.addEventListener(
    "pointerdown",
    (e) => {
      const el = (e.target as HTMLElement | null)?.closest?.<HTMLElement>("button, [role=button], a, .mi");
      if (el) pressed = { el, at: Date.now() };
    },
    true,
  );
function anchorNow(): HTMLElement | null {
  const a = document.activeElement as HTMLElement | null;
  let el = a && a !== document.body && a.closest("button, [role=button], a, .mi") ? a : pressed && Date.now() - pressed.at < 3000 && pressed.el.isConnected ? pressed.el : null;
  // asked from a menu that closes as it asks: below the row the menu was for
  const host = el?.closest<HTMLElement>(".popin-host");
  if (host) el = (host.previousElementSibling ?? host.nextElementSibling) as HTMLElement | null;
  return el;
}

/** A confirmation asked with the app's own dialog (theme, language, Esc),
 *  instead of the browser's window.confirm. Resolves true on OK. */
export interface ConfirmOptions {
  /** the question; a first line becomes the title, the rest the body */
  text: string;
  /** label of the confirming button (default: OK in the UI's language) */
  ok?: string;
  /** a destructive action: the button is red */
  danger?: boolean;
}

type Pending = ConfirmOptions & { done: (v: boolean) => void; anchor: HTMLElement | null };

let show: ((p: Pending) => void) | null = null;

/** The button for a question that starts with its verb ("删除 X？",
 *  "Disconnect X?"): that verb, which says what will happen; else OK. */
const VERBS_ZH = ["确定删除", "删除", "移除", "断开", "放弃", "退出登录", "撤销", "归档", "解散", "停止"];
function verbOf(text: string): string | null {
  const s = text.trim();
  for (const v of VERBS_ZH) if (s.startsWith(v)) return v.replace(/^确定/, "");
  const m = /^(Delete|Remove|Disconnect|Discard|Sign out|Revoke|Archive|Dissolve|Stop)\b/i.exec(s);
  return m ? m[1][0].toUpperCase() + m[1].slice(1) : null;
}

export function splitQuestion(s: string): [string, string] {
  let i = s.indexOf("？");
  if (i < 0) {
    const m = /\?(?=\s+[A-Z\u4e00-\u9fff]|\s*$)/.exec(s);
    i = m ? m.index : -1;
  }
  if (i < 0) {
    const [first, ...rest] = s.split("\n");
    return [first, rest.join("\n").trim()];
  }
  return [s.slice(0, i + 1).trim(), s.slice(i + 1).trim()];
}

/** Asks; falls back to window.confirm before the host is mounted. */
export function confirmDialog(o: ConfirmOptions | string): Promise<boolean> {
  const opts = typeof o === "string" ? { text: o, danger: true } : o;
  if (!show) return Promise.resolve(window.confirm(opts.text));
  const anchor = anchorNow();
  return new Promise((done) => show!({ ...opts, done, anchor }));
}

/** Mounted once (App), renders the open confirmation. */
export function ConfirmHost() {
  const [p, setP] = useState<Pending | null>(null);
  const okRef = useRef<HTMLButtonElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    show = (x) => setP(x);
    return () => {
      show = null;
    };
  }, []);
  useEffect(() => {
    // a destructive answer is never one stray Enter away (after it is
    // placed: in place it appears a moment later)
    if (!p) return;
    const id = window.setTimeout(() => (p.danger ? cancelRef : okRef).current?.focus({ preventScroll: true }), 30);
    return () => window.clearTimeout(id);
  }, [p]);
  if (!p) return null;
  const answer = (v: boolean) => {
    setP(null);
    p.done(v);
  };
  // "删除 X？工作区会保留。" → title "删除 X？", body the rest. zh asks
  // with a full-width ？ (names typed with an ASCII "?" stay whole); en with
  // a "?" ending the text or followed by a new sentence.
  const [title, body] = splitQuestion(p.text);
  const parts = (
    <>
      <div className="mhd">
        <b>{title}</b>
      </div>
      {body && (
        <div className="mbd">
          <p className="small" style={{ margin: 0, whiteSpace: "pre-line" }}>
            {body}
          </p>
        </div>
      )}
      <div className="mft">
        <button ref={cancelRef} className="btn ghost" type="button" onClick={() => answer(false)}>
          {t("common.cancel")}
        </button>
        <button ref={okRef} className={"btn " + (p.danger ? "dngfill" : "pri")} type="button" onClick={() => answer(true)}>
          {p.ok ?? verbOf(p.text) ?? t("common.ok")}
        </button>
      </div>
    </>
  );
  // in place, below what asked it (a click outside or Esc answers no);
  // a dialog only when nothing on the page asked
  if (p.anchor?.isConnected)
    return (
      <Popover anchor={p.anchor} open onClose={() => answer(false)} width={360} align="end" className="cfm cfmin">
        <div role="alertdialog" aria-label={title}>
          {parts}
        </div>
      </Popover>
    );
  return (
    <>
      <div className="scrim" onClick={() => answer(false)} />
      <div
        className="modal sm cfm"
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            answer(false);
          }
        }}
      >
        <div className="mhd">
          <b>{title}</b>
        </div>
        {body && (
          <div className="mbd">
            <p className="small" style={{ margin: 0, whiteSpace: "pre-line" }}>
              {body}
            </p>
          </div>
        )}
        <div className="mft">
          <button ref={cancelRef} className="btn ghost" type="button" onClick={() => answer(false)}>
            {t("common.cancel")}
          </button>
          <button ref={okRef} className={"btn " + (p.danger ? "dngfill" : "pri")} type="button" onClick={() => answer(true)}>
            {p.ok ?? verbOf(p.text) ?? t("common.ok")}
          </button>
        </div>
      </div>
    </>
  );
}
