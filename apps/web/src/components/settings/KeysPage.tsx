// 设置 › 快捷键: every shortcut, grouped, rebindable, with Enter-to-send.

import { useEffect, useState } from "react";
import { Head, Row } from "./Head";
import { t, type Key } from "../../i18n";
import { SHORTCUTS, binding, comboOf, conflict, display, enterSends, fixedKeys, isCustom, onShortcutsChange, resetAll, resetBinding, setBinding, setEnterSends, type ActionId, type Shortcut } from "../../shortcuts";

const GROUPS: { id: Shortcut["group"]; title: Key }[] = [
  { id: "general", title: "keys.general" },
  { id: "nav", title: "keys.nav" },
  { id: "chat", title: "keys.chat" },
  { id: "panel", title: "keys.panel" },
];

function Combo({ combo }: { combo: string }) {
  const parts = display(combo);
  if (!parts.length) return <span className="small muted">{t("keys.none")}</span>;
  return (
    <span className="kcombo">
      {parts.map((p, i) => (
        <kbd key={i} className="kbig">
          {p}
        </kbd>
      ))}
    </span>
  );
}

/** 设置 › 快捷键: every shortcut, click one to record a new key. */
export function Keys() {
  const [, bump] = useState(0);
  const [rec, setRec] = useState<ActionId | null>(null);
  const [msg, setMsg] = useState("");
  useEffect(() => onShortcutsChange(() => bump((n) => n + 1)), []);
  useEffect(() => {
    if (!rec) return;
    const key = (e: KeyboardEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (e.key === "Escape") {
        setRec(null);
        setMsg("");
        return;
      }
      if (e.key === "Backspace" || e.key === "Delete") {
        setBinding(rec, "");
        setRec(null);
        setMsg("");
        return;
      }
      const c = comboOf(e);
      if (!c) return; // a modifier alone: keep waiting
      const why = conflict(rec, c);
      if (why === "reserved") {
        setMsg(t("keys.reserved", { keys: display(c).join("") }));
        return;
      }
      if (why === "needsModifier") {
        setMsg(t("keys.needsModifier"));
        return;
      }
      if (why) {
        const other = SHORTCUTS.find((s) => s.id === why);
        // swap: the other action takes this one's old key
        setBinding(why as ActionId, binding(rec));
        setMsg(t("keys.swapped", { other: other ? t(other.label) : why }));
      } else setMsg("");
      setBinding(rec, c);
      setRec(null);
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [rec]);
  const fixed = fixedKeys();
  const custom = SHORTCUTS.some((s) => isCustom(s.id));
  return (
    <>
      <Head
        title={t("keys.title")}
        sub={t("keys.sub")}
        right={
          custom ? (
            <button className="btn sm" type="button" onClick={() => resetAll()}>
              {t("keys.resetAll")}
            </button>
          ) : undefined
        }
      />
      <div className="vb">
        <p className="small" style={{ margin: "0 0 10px" }}>
          {rec ? t("keys.recording") : t("keys.howto")}
        </p>
        {msg && <p className="small warnq">{msg}</p>}
        {GROUPS.map((g) => (
          <div key={g.id} className="kgrp">
            <div className="vsec">{t(g.title)}</div>
            <div className="sbox">
              {SHORTCUTS.filter((s) => s.group === g.id).map((s) => (
                <Row key={s.id} title={t(s.label)}>
                  <span className="krow">
                    <button
                      type="button"
                      className={"kbtn" + (rec === s.id ? " rec" : "")}
                      data-recording={rec === s.id ? "" : undefined}
                      onClick={() => {
                        setMsg("");
                        setRec(rec === s.id ? null : s.id);
                      }}
                      title={t("keys.change")}
                    >
                      {rec === s.id ? <span className="small">{t("keys.pressKeys")}</span> : <Combo combo={binding(s.id)} />}
                    </button>
                    {isCustom(s.id) && (
                      <button className="btn sm ghost" type="button" onClick={() => resetBinding(s.id)} title={t("keys.reset")}>
                        {t("keys.reset")}
                      </button>
                    )}
                  </span>
                </Row>
              ))}
              {fixed
                .filter((f) => f.group === g.id)
                .map((f) => (
                  <Row key={f.label} title={t(f.label)} dim>
                    <Combo combo={f.keys} />
                  </Row>
                ))}
              {g.id === "chat" && (
                <Row title={t("keys.enterSends")} desc={t("keys.enterSendsDesc")}>
                  <button className="tg" type="button" role="switch" aria-checked={enterSends()} aria-label={t("keys.enterSends")} onClick={() => setEnterSends(!enterSends())} />
                </Row>
              )}
            </div>
          </div>
        ))}
      </div>
    </>
  );
}
