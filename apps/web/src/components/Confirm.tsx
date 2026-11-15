import { useEffect, useRef, useState } from "react";
import { t } from "../i18n";

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

type Pending = ConfirmOptions & { done: (v: boolean) => void };

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

/** Asks; falls back to window.confirm before the host is mounted. */
export function confirmDialog(o: ConfirmOptions | string): Promise<boolean> {
  const opts = typeof o === "string" ? { text: o, danger: true } : o;
  if (!show) return Promise.resolve(window.confirm(opts.text));
  return new Promise((done) => show!({ ...opts, done }));
}

/** Mounted once (App), renders the open confirmation. */
export function ConfirmHost() {
  const [p, setP] = useState<Pending | null>(null);
  const okRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    show = (x) => setP(x);
    return () => {
      show = null;
    };
  }, []);
  useEffect(() => {
    if (p) okRef.current?.focus();
  }, [p]);
  if (!p) return null;
  const answer = (v: boolean) => {
    setP(null);
    p.done(v);
  };
  // "删除 X？工作区会保留。" → title "删除 X？", body the rest
  const m = /^([^\n]*?[？?])\s*([\s\S]*)$/.exec(p.text);
  const title = m ? m[1] : p.text.split("\n")[0];
  const body = (m ? m[2] : p.text.split("\n").slice(1).join("\n")).trim();
  return (
    <>
      <div className="scrim" onClick={() => answer(false)} />
      <div
        className="modal sm"
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
          <button className="btn ghost" type="button" onClick={() => answer(false)}>
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
