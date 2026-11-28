// 做成技能: a finished turn's work becomes a skill (SKILL.md). A cheap
// model drafts it on the server (…/skill-draft) from the same stretch of
// conversation 做成例行 reads; the draft opens under the turn in the same
// card as 做成例行, every part editable, and 保存 writes it for this agent
// or for every agent (POST /api/skills). Nothing is saved until then.

import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { ArrowUpRight, Check, RefreshCw, Sparkles, X } from "lucide-react";
import { API, type SkillDraft } from "../../api";
import { t } from "../../i18n";
import { openSettings, toast, useStore } from "../../store";

type Scope = "turn" | "all";
interface Anchor {
  item: string;
  agent: string;
  upto: number;
}
let anchors: Record<string, Anchor | undefined> = {};
const subs = new Set<() => void>();
const subscribe = (f: () => void) => {
  subs.add(f);
  return () => void subs.delete(f);
};
function setAnchor(session: string, a: Anchor | undefined) {
  anchors = { ...anchors, [session]: a };
  subs.forEach((f) => f());
}
const useAnchor = (session: string) => useSyncExternalStore(subscribe, () => anchors[session]);

const NAME = /^[a-z0-9][a-z0-9-]{0,63}$/;
const errMsg = (e: any) => e?.message ?? String(e);

/** 「做成技能」 among a finished turn's actions. */
export function SkillDraftBtn({ session, item, agent }: { session: string; item: string; agent: string }) {
  const cur = useAnchor(session);
  const on = cur?.item === item;
  return (
    <button
      type="button"
      className={"ix mact c2r-btn" + (on ? " on" : "")}
      aria-expanded={on}
      aria-label={t("sk2.btn")}
      title={t("sk2.btnTitle")}
      onClick={() => setAnchor(session, on ? undefined : { item, agent, upto: Number(item) })}
    >
      <Sparkles size={14} />
    </button>
  );
}

/** The card, if this answer holds the session's skill draft. */
export function SkillDraftSlot({ session, item }: { session: string; item: string }) {
  const a = useAnchor(session);
  if (!a || a.item !== item) return null;
  return <SkillDraftCard key={item} session={session} anchor={a} onClose={() => setAnchor(session, undefined)} />;
}

function SkillDraftCard({ session, anchor, onClose }: { session: string; anchor: Anchor; onClose: () => void }) {
  const agentName = useStore((s) => s.agents[anchor.agent]?.name ?? anchor.agent);
  const [scope, setScope] = useState<Scope>("turn");
  const [nonce, setNonce] = useState(0);
  const [draft, setDraft] = useState<SkillDraft | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [content, setContent] = useState("");
  const [forAll, setForAll] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<string | null>(null);
  const ref = useRef<HTMLElement>(null);

  useEffect(() => {
    let live = true;
    setDraft(null);
    setErr(null);
    API.skillDraft(session, { agent: anchor.agent, upto: anchor.upto, scope }).then(
      (d) => {
        if (!live) return;
        setDraft(d);
        setName(d.name);
        setContent(d.content);
      },
      (e) => live && setErr(errMsg(e)),
    );
    return () => {
      live = false;
    };
  }, [session, anchor.agent, anchor.upto, scope, nonce]);
  // opened below the answer: in view
  useEffect(() => {
    ref.current?.scrollIntoView({ block: "nearest", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  }, [!!draft]);

  // the name in the front matter follows the folder name
  const setFolder = (v: string) => {
    const n = v.toLowerCase().replace(/[^a-z0-9-]/g, "-");
    setName(n);
    setContent((c) => c.replace(/^(---\n(?:[^\n]*\n)*?)name:[^\n]*\n/, `$1name: ${n}\n`));
  };
  const badName = !!name && !NAME.test(name);
  const save = async () => {
    if (!NAME.test(name) || !content.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      await API.saveSkill({ name, content, agent: forAll ? undefined : anchor.agent });
      setSaved(name);
      toast(t("sk2.saved", { name }));
    } catch (e: any) {
      setErr(e?.status === 409 ? t("sk2.exists", { name }) : errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  if (saved)
    return (
      <section className="c2r done" ref={ref} aria-label={t("sk2.title")}>
        <header className="c2r-h">
          <span className="c2r-i ok" aria-hidden>
            <Check size={15} />
          </span>
          <div className="g">
            <b>{t("sk2.savedTitle", { name: saved })}</b>
            <span className="small">{forAll ? t("sk2.savedAll") : t("sk2.savedOne", { name: agentName })}</span>
          </div>
          <button className="ix" type="button" aria-label={t("common.close")} title={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </header>
        <footer className="c2r-foot">
          <span className="sp" />
          <button className="btn sm" type="button" onClick={() => openSettings("market")}>
            <ArrowUpRight size={12} />
            {t("sk2.see")}
          </button>
        </footer>
      </section>
    );

  return (
    <section className="c2r" ref={ref} aria-label={t("sk2.title")}>
      <header className="c2r-h">
        <span className="c2r-i" aria-hidden>
          <Sparkles size={15} />
        </span>
        <div className="g">
          <b>{t("sk2.title")}</b>
          <span className="small">{t("sk2.sub")}</span>
        </div>
        <button className="ix" type="button" aria-label={t("common.close")} title={t("common.close")} onClick={onClose}>
          <X size={14} />
        </button>
      </header>
      <div className="c2r-bar">
        <div className="seg" role="group" aria-label={t("c2r.scope")}>
          {(["turn", "all"] as const).map((s) => (
            <button key={s} type="button" aria-pressed={scope === s} onClick={() => setScope(s)}>
              {t(s === "turn" ? "c2r.scopeTurn" : "c2r.scopeAll")}
            </button>
          ))}
        </div>
        <span className="sp" />
        {draft && (
          <button className="btn ghost sm" type="button" onClick={() => setNonce((n) => n + 1)} title={t("c2r.redraftTitle")}>
            <RefreshCw size={12} />
            {t("c2r.redraft")}
          </button>
        )}
      </div>

      {!draft && !err && (
        <div className="c2r-load" role="status" aria-live="polite">
          <span className="c2r-pulse" aria-hidden />
          <span>{t("sk2.reading")}</span>
          <div className="c2r-lines" aria-hidden>
            <i />
            <i />
            <i />
          </div>
        </div>
      )}
      {!draft && err && (
        <div className="c2r-err">
          <span>{t("c2r.failed", { error: err })}</span>
          <button className="btn sm" type="button" onClick={() => setNonce((n) => n + 1)}>
            <RefreshCw size={12} />
            {t("c2r.retry")}
          </button>
        </div>
      )}

      {draft && (
        <>
          <div className="fgrid c2r-grid">
            <label className="fl" htmlFor="sk2-name">
              {t("sk2.name")}
            </label>
            <div>
              <input id="sk2-name" className="fi sm mono" value={name} aria-invalid={badName} onChange={(e) => setFolder(e.target.value)} />
              <span className={"small" + (badName ? " errt" : "")}>{t("sk2.nameHint")}</span>
            </div>
            <label className="fl" htmlFor="sk2-body">
              SKILL.md
            </label>
            <div>
              <textarea id="sk2-body" className="fi sm mono" rows={14} spellCheck={false} value={content} onChange={(e) => setContent(e.target.value)} />
              <span className="small">{t("sk2.bodyHint")}</span>
            </div>
            <label className="fl">{t("sk2.for")}</label>
            <div className="seg" role="group" aria-label={t("sk2.for")}>
              <button type="button" aria-pressed={!forAll} onClick={() => setForAll(false)}>
                {t("sk2.forOne", { name: agentName })}
              </button>
              <button type="button" aria-pressed={forAll} onClick={() => setForAll(true)}>
                {t("sk2.forAll")}
              </button>
            </div>
          </div>
          {err && <p className="small errt" style={{ margin: "0 14px 10px" }}>{err}</p>}
          <footer className="c2r-foot">
            {draft.model && <span className="small muted">{t("sk2.by", { model: draft.model })}</span>}
            <span className="sp" />
            <button className="btn ghost sm" type="button" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button className="btn pri sm" type="button" disabled={busy || !NAME.test(name) || !content.trim()} onClick={() => void save()}>
              {busy ? t("sk2.saving") : t("sk2.save")}
            </button>
          </footer>
        </>
      )}
    </section>
  );
}
