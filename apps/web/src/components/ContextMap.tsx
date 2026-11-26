// The context window map: what the next request of an agent carries, as a
// treemap (or a sorted list) of prompt sections, tools, tool calls and
// messages, with the content of any block one click away.

import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { Check, Copy, LayoutGrid, List, Minimize2, Plus, X } from "lucide-react";
import { API, type AgentView, type ContextBreakdown, type ContextItem, type ContextItemContent, type ModelRef } from "../api";
import { useCopy } from "../clipboard";
import { kfmt, modelLabel } from "../format";
import { t, type Key } from "../i18n";
import { openSession, refreshAgents, refreshSessions, toast, useStore } from "../store";
import { squarify, type Rect } from "../treemap";
import { Markdown } from "./Markdown";
import { ProviderMark } from "./ProviderMark";

export const SEG_COLORS: Record<string, string> = {
  system: "var(--seg-system)",
  memory: "var(--seg-memory)",
  tools: "var(--seg-tools)",
  skills: "var(--seg-skills)",
  toolio: "var(--seg-toolio)",
  messages: "var(--seg-messages)",
  other: "var(--cat-8)",
  free: "var(--active)",
};

const KINDS = ["system", "memory", "skills", "tools", "toolio", "messages", "other", "free"];

/** Label of a segment / item kind. */
export function kindLabel(key: string, fallback?: string): string {
  const k = `ctx.seg.${key}` as Key;
  const v = t(k);
  return v === k ? (fallback ?? key) : v;
}

/** A block of the map: a server item, a client roll-up of small ones, or free space. */
interface Block extends ContextItem {
  members?: ContextItem[];
}

function pctOf(n: number, of: number): string {
  if (!of) return "0";
  const p = (n / of) * 100;
  return p >= 10 ? String(Math.round(p)) : p >= 1 ? p.toFixed(1).replace(/\.0$/, "") : p > 0 ? "<1" : "0";
}

/** Status of the context against the auto-compaction threshold. */
function status(bd: ContextBreakdown): "ok" | "near" | "over" {
  const r = bd.window ? bd.used / bd.window : 0;
  const thr = bd.threshold || 0.8;
  if (bd.overThreshold || r >= thr) return "over";
  if (r >= Math.min(0.6, thr * 0.75)) return "near";
  return "ok";
}

function itemLabel(it: ContextItem): string {
  if (it.count) return t("cmap.more", { n: it.count });
  if (it.kind === "other") return kindLabel(it.kind);
  // MCP tools are "<server>__<tool>": the server is the group, shown apart
  if (it.kind === "tools" && it.group && it.label.startsWith(it.group + "__")) return it.label.slice(it.group.length + 2);
  if (it.kind === "messages" && it.group) {
    const role = t(`cmap.role.${it.group}` as Key);
    return role.startsWith("cmap.") ? it.label : `${role} · ${it.label}`;
  }
  return it.label;
}

/** Items of each kind (from the server, or one block per segment from an older one). */
function itemsByKind(bd: ContextBreakdown): Map<string, ContextItem[]> {
  const out = new Map<string, ContextItem[]>();
  for (const it of bd.items ?? []) {
    if (it.tokens <= 0) continue;
    out.set(it.kind, [...(out.get(it.kind) ?? []), it]);
  }
  for (const s of bd.segments) {
    if (s.key === "free" || s.tokens <= 0 || out.has(s.key)) continue;
    out.set(s.key, [{ id: "seg:" + s.key, kind: s.key, label: kindLabel(s.key, s.label), tokens: s.tokens }]);
  }
  return out;
}

const MIN_BLOCK = 22 * 22; // px²: smaller items fold into "N more"

/** Lays out the two-level map: kinds, then the items inside each kind. */
function layout(bd: ContextBreakdown, w: number, h: number, withFree: boolean): { b: Block; r: Rect }[] {
  if (w <= 0 || h <= 0) return [];
  const byKind = itemsByKind(bd);
  const kinds = KINDS.filter((k) => (k === "free" ? withFree && bd.free > 0 : (byKind.get(k) ?? []).length > 0));
  const totals = kinds.map((k) => (k === "free" ? bd.free : (byKind.get(k) ?? []).reduce((s, it) => s + it.tokens, 0)));
  const kr = squarify(totals, { x: 0, y: 0, w, h });
  const out: { b: Block; r: Rect }[] = [];
  kinds.forEach((k, i) => {
    const r = kr[i];
    if (r.w <= 0 || r.h <= 0) return;
    if (k === "free") {
      out.push({ b: { id: "free", kind: "free", label: kindLabel("free"), tokens: bd.free }, r });
      return;
    }
    const items = [...(byKind.get(k) ?? [])].sort((a, b) => b.tokens - a.tokens);
    const per = (r.w * r.h) / Math.max(1, totals[i]);
    const keep: Block[] = [];
    const small: ContextItem[] = [];
    for (const it of items) (it.tokens * per < MIN_BLOCK || it.count ? small : keep).push(it);
    if (small.length === 1 && !small[0].count) keep.push(small[0]);
    else if (small.length) {
      const n = small.reduce((s, it) => s + (it.count || 1), 0);
      keep.push({ id: "fold:" + k, kind: k, label: "", tokens: small.reduce((s, it) => s + it.tokens, 0), count: n, members: small });
    }
    const ir = squarify(
      keep.map((b) => b.tokens),
      r,
    );
    keep.forEach((b, j) => ir[j].w > 0 && out.push({ b, r: ir[j] }));
  });
  return out;
}

export function ContextMap({
  agent,
  sessionId,
  channel,
  ctx,
  onClose,
  inline,
}: {
  agent: AgentView;
  sessionId: string;
  channel: boolean;
  ctx?: { tokens: number; window: number; model?: ModelRef };
  onClose: () => void;
  /** in place (the composer's tray gives it a frame and closes it) */
  inline?: boolean;
}) {
  const models = useStore((s) => s.models);
  const evCount = useStore((s) => s.events[sessionId]?.length ?? 0);
  const [bd, setBd] = useState<ContextBreakdown | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<"" | "compact" | "new">("");
  // the map and its parts: for whoever wants to look closer
  const [detail, setDetail] = useState(() => {
    try {
      return localStorage.getItem("crewbase.ctxDetail") === "1";
    } catch {
      return false;
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem("crewbase.ctxDetail", detail ? "1" : "0");
    } catch {
      /* private mode */
    }
  }, [detail]);
  const [view, setView] = useState<"map" | "list">(() => {
    try {
      return localStorage.getItem("crewbase.ctxView") === "list" ? "list" : "map";
    } catch {
      return "map";
    }
  });
  const [kind, setKind] = useState<string | null>(null);
  // Free space is drawn to scale; while most of the window is free it
  // would squeeze everything else into a strip, so it starts hidden then.
  const [showFree, setShowFree] = useState<boolean | null>(null);
  const [sel, setSel] = useState<Block | null>(null);

  const load = () =>
    API.context(agent.id, sessionId)
      .then((b) => {
        setBd(b);
        setErr(null);
      })
      .catch((e) => setErr(e?.message ?? String(e)));
  useEffect(() => {
    void load();
  }, [agent.id, sessionId, evCount]);
  useEffect(() => {
    // Esc first closes a block's detail; then the whole view (in place,
    // the tray does that: this runs first, in the capture phase)
    const k = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (sel) {
        e.preventDefault();
        e.stopPropagation();
        setSel(null);
      } else if (!inline) {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", k, inline);
    return () => window.removeEventListener("keydown", k, inline);
  }, [onClose, sel, inline]);
  const pickView = (v: "map" | "list") => {
    setView(v);
    try {
      localStorage.setItem("crewbase.ctxView", v);
    } catch {
      /* private mode */
    }
  };

  const compact = async () => {
    setBusy("compact");
    try {
      await API.compact(agent.id, sessionId);
      toast(t("ctx.compacted", { name: agent.name }));
      setSel(null);
      await load();
    } catch (e: any) {
      toast(t("ctx.compactFailed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy("");
    }
  };
  const fresh = async () => {
    setBusy("new");
    try {
      const m = await API.newConversation(agent.id);
      await Promise.allSettled([refreshAgents(), refreshSessions()]);
      onClose();
      openSession(m.id);
      toast(t("ctx.newChatDone", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  };

  const win = bd?.window || ctx?.window || 0;
  const used = bd?.used ?? ctx?.tokens ?? 0;
  const model = bd?.model?.provider ? bd.model : ctx?.model;
  const st = bd ? status(bd) : "ok";
  const totals = useMemo(() => {
    const m = new Map<string, number>();
    for (const s of bd?.segments ?? []) if (s.tokens > 0) m.set(s.key, s.tokens);
    return m;
  }, [bd]);
  const withFree = showFree ?? (win > 0 && used / win >= 0.35);

  const body = (
      <div className={(inline ? "ctxmap inline" : "modal ctxmap") + (detail ? "" : " brief")} role={inline ? undefined : "dialog"} aria-modal={inline ? undefined : true} aria-labelledby={inline ? undefined : "cmapTitle"}>
        {!inline && (
          <div className="mhd">
            <h3 id="cmapTitle">{t("cmap.title")}</h3>
            <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
              <X size={15} />
            </button>
          </div>
        )}
        <div className="mbd cxbody">
          {!bd && !err && (
            <div className="cxempty">
              <span className="spin sm" /> {t("common.loading")}
            </div>
          )}
          {!bd && err && <div className="cxempty">{t("cmap.failed", { error: err })}</div>}
          {bd && (
            <>
              <div className="cxhead">
                <div className="cxused">
                  <div className="cxeyebrow">
                    {t("cmap.used")}
                    <span className={"pill sm " + (st === "over" ? "red" : st === "near" ? "amber" : "ok")}>{t(`cmap.st.${st}` as Key)}</span>
                  </div>
                  <div className="cxbig">
                    <b>{kfmt(used)}</b> <span>{t("cmap.of", { total: kfmt(win) })}</span>
                  </div>
                  <div className="cxsub">
                    {t("cmap.full", { pct: pctOf(used, win) })}
                    {bd.headroom ? " · " + t("cmap.headroom", { n: bd.headroom }) : ""}
                  </div>
                </div>
                {bd.cache && detail && (
                  <div className="cxcache">
                    <div className="cxeyebrow">{t("cmap.cache")}</div>
                    <div className="cxbig sm">
                      <b>{Math.round(bd.cache.hitPercent)}%</b> <span>{t("cmap.hit")}</span>
                    </div>
                    <div className="cxcbar" role="img" aria-label={t("cmap.hitAria", { pct: Math.round(bd.cache.hitPercent) })}>
                      <i style={{ width: Math.min(100, bd.cache.hitPercent) + "%" }} />
                    </div>
                    <div className="cxsub">
                      {bd.cache.read === 0 && bd.cache.write > 0
                        ? t("cmap.cacheWarm", { n: kfmt(bd.cache.write) })
                        : bd.cache.read === 0
                          ? t("cmap.cacheNone")
                          : t("cmap.cacheSplit", { cached: kfmt(bd.cache.read), fresh: kfmt(Math.max(0, bd.cache.input - bd.cache.read)) })}
                    </div>
                  </div>
                )}
                <div className="cxmodel" hidden={!detail}>
                  {model && (
                    <span className="cxmname">
                      <ProviderMark provider={model.provider} model={model.model} size={14} />
                      <span>{modelLabel(models, model)}</span>
                    </span>
                  )}
                  <span className={"pill sm " + (bd.counted ? "blue" : "")} title={t(bd.counted ? "cmap.countedTip" : "cmap.estimatedTip")}>
                    {t(bd.counted ? "cmap.counted" : "cmap.estimated")}
                  </span>
                </div>
              </div>

              {/* in plain words first; the map is the detail, on request */}
              <p className="cxplain">{t(st === "over" ? "cmap.plainOver" : st === "near" ? "cmap.plainNear" : "cmap.plainOk", { name: agent.name })}</p>
              {!detail && (
                <button type="button" className="linkbtn cxmore" onClick={() => setDetail(true)}>
                  {t("cmap.showDetail")}
                </button>
              )}
              {detail && (<>
              <div className="cxbar">
                <div className="cxlegend" role="group" aria-label={t("cmap.legend")}>
                  {KINDS.filter((k) => totals.has(k)).map((k) =>
                    k === "free" ? (
                      <button
                        key={k}
                        type="button"
                        className={"cxleg" + (withFree ? " on" : " dim")}
                        aria-pressed={withFree}
                        title={t(withFree ? "cmap.freeHide" : "cmap.freeShow")}
                        onClick={() => setShowFree(!withFree)}
                      >
                        <i className="free" />
                        <span>{kindLabel(k)}</span>
                        <b>{kfmt(totals.get(k) ?? 0)}</b>
                      </button>
                    ) : (
                    <button
                      key={k}
                      type="button"
                      className={"cxleg" + (kind === k ? " on" : "") + (kind && kind !== k ? " dim" : "")}
                      aria-pressed={kind === k}
                      title={t("cmap.legendTip")}
                      onClick={() => setKind(kind === k ? null : k)}
                    >
                      <i style={{ background: SEG_COLORS[k] }} />
                      <span>{kindLabel(k)}</span>
                      <b>{kfmt(totals.get(k) ?? 0)}</b>
                    </button>
                    ),
                  )}
                </div>
                <div className="seg" role="group" aria-label={t("cmap.viewAria")}>
                  <button type="button" aria-pressed={view === "map"} onClick={() => pickView("map")} title={t("cmap.viewMap")}>
                    <LayoutGrid size={14} />
                    <span className="cxvl">{t("cmap.viewMap")}</span>
                  </button>
                  <button type="button" aria-pressed={view === "list"} onClick={() => pickView("list")} title={t("cmap.viewList")}>
                    <List size={14} />
                    <span className="cxvl">{t("cmap.viewList")}</span>
                  </button>
                </div>
              </div>

              <div className={"cxmain" + (sel ? " withd" : "")}>
                <div className="cxview">
                  {view === "map" ? <Treemap bd={bd} kind={kind} sel={sel} withFree={withFree} onPick={setSel} /> : <ItemList bd={bd} kind={kind} sel={sel} onPick={setSel} />}
                  <p className="small cxhint">
                    {view === "map" && !withFree && bd.free > 0 ? t("cmap.freeHidden", { free: kfmt(bd.free) }) + " " : ""}
                    {!sel ? t("cmap.pick") : ""}
                  </p>
                </div>
                {sel && <Detail agent={agent} sessionId={sessionId} bd={bd} block={sel} onPick={setSel} onClose={() => setSel(null)} />}
              </div>

              {bd.recorded && <p className="small cxnote">{t("cmap.recorded")}</p>}
              <p className="small cxnote">
                {t("ctx.note")}
                {bd.compactions > 0 ? " " + t("ctx.compactions", { n: bd.compactions }) : ""}
              </p>
              </>)}
              {bd.overThreshold && <p className="small warnline cxnote">{t("ctx.over")}</p>}
            </>
          )}
        </div>
        <div className="mft cxft">
          <button className="btn" type="button" disabled={!!busy || !bd} onClick={compact} title={t("ctx.compactDesc")}>
            {busy === "compact" ? <span className="spin sm" /> : <Minimize2 size={14} />}
            {t("ctx.compact")}
          </button>
          {!channel && (
            <button className="btn" type="button" disabled={!!busy} onClick={fresh} title={t("ctx.newChatDesc", { name: agent.name })}>
              {busy === "new" ? <span className="spin sm" /> : <Plus size={14} />}
              {t("ctx.newChat")}
            </button>
          )}
        </div>
      </div>
  );
  if (inline) return body;
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      {body}
    </>,
    document.body,
  );
}

function blockColor(k: string): string {
  return SEG_COLORS[k] ?? "var(--muted)";
}

function Treemap({ bd, kind, sel, withFree, onPick }: { bd: ContextBreakdown; kind: string | null; sel: Block | null; withFree: boolean; onPick: (b: Block) => void }) {
  const ref = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  const [tip, setTip] = useState<{ b: Block; x: number; y: number } | null>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const set = () => setSize({ w: el.clientWidth, h: el.clientHeight });
    set();
    const ro = new ResizeObserver(set);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const blocks = useMemo(() => layout(bd, size.w, size.h, withFree), [bd, size.w, size.h, withFree]);
  const total = Math.max(bd.window, bd.used);
  return (
    <div className="cxmap" ref={ref} role="list" aria-label={t("cmap.mapAria")} onMouseLeave={() => setTip(null)}>
      {blocks.map(({ b, r }) => {
        const free = b.kind === "free";
        const big = r.w >= 66 && r.h >= 34;
        const name = r.w >= 50 && r.h >= 18;
        const style: CSSProperties = { left: r.x, top: r.y, width: Math.max(0, r.w - 2), height: Math.max(0, r.h - 2) };
        if (!free) (style as Record<string, string | number>)["--c"] = blockColor(b.kind);
        const label = free ? kindLabel("free") : itemLabel(b);
        const cls = "cxblk" + (free ? " free" : "") + (kind && kind !== b.kind ? " dim" : "") + (sel?.id === b.id ? " on" : "") + (b.kind === "other" ? " opaque" : "");
        const aria = `${label} · ${kfmt(b.tokens)} · ${pctOf(b.tokens, total)}%`;
        const inner = (
          <>
            {name && <span className="n">{label}</span>}
            {big && (
              <span className="v">
                {kfmt(b.tokens)} · {free ? pctOf(b.tokens, total) + "%" : kindLabel(b.kind)}
              </span>
            )}
          </>
        );
        const hover = (e: React.MouseEvent) => {
          const box = ref.current?.getBoundingClientRect();
          if (box) setTip({ b, x: e.clientX - box.left, y: e.clientY - box.top });
        };
        return free ? (
          <div key={b.id} role="listitem" className={cls} style={style} aria-label={aria} onMouseMove={hover}>
            {inner}
          </div>
        ) : (
          <button key={b.id} type="button" role="listitem" className={cls} style={style} aria-label={aria} onMouseMove={hover} onFocus={() => setTip(null)} onClick={() => onPick(b)}>
            {inner}
          </button>
        );
      })}
      {tip && (
        <div className={"cxtip" + (tip.x > size.w - 200 ? " l" : "")} style={{ left: tip.x, top: tip.y }}>
          <b>{tip.b.kind === "free" ? kindLabel("free") : itemLabel(tip.b)}</b>
          {tip.b.group && tip.b.kind !== "messages" && <span className="g">{tip.b.group}</span>}
          <span>
            {t("cmap.pctOf", { tokens: tip.b.tokens.toLocaleString(), pct: pctOf(tip.b.tokens, total) })}
          </span>
        </div>
      )}
    </div>
  );
}

function ItemList({ bd, kind, sel, onPick }: { bd: ContextBreakdown; kind: string | null; sel: Block | null; onPick: (b: Block) => void }) {
  const all = useMemo(() => {
    const xs: ContextItem[] = [];
    for (const its of itemsByKind(bd).values()) xs.push(...its);
    return xs.sort((a, b) => b.tokens - a.tokens);
  }, [bd]);
  const rows = kind && kind !== "free" ? all.filter((it) => it.kind === kind) : all;
  const top = rows[0]?.tokens || 1;
  const total = Math.max(bd.window, bd.used);
  if (!rows.length) return <div className="cxlist cxempty">{t("cmap.empty")}</div>;
  return (
    <div className="cxlist" role="list">
      {rows.map((it) => (
        <button key={it.id} type="button" role="listitem" className={"cxrow" + (sel?.id === it.id ? " on" : "")} onClick={() => onPick(it)}>
          <i className="dot" style={{ background: blockColor(it.kind) }} />
          <span className="g">
            <span className="n">{itemLabel(it)}</span>
            <span className="k">
              {kindLabel(it.kind)}
              {it.group && it.kind !== "messages" ? " · " + it.group : ""}
            </span>
          </span>
          <span className="bar" aria-hidden="true">
            <i style={{ width: Math.max(1, (it.tokens / top) * 100) + "%", background: blockColor(it.kind) }} />
          </span>
          <b>{kfmt(it.tokens)}</b>
          <span className="p">{pctOf(it.tokens, total)}%</span>
        </button>
      ))}
    </div>
  );
}

function Detail({
  agent,
  sessionId,
  bd,
  block,
  onPick,
  onClose,
}: {
  agent: AgentView;
  sessionId: string;
  bd: ContextBreakdown;
  block: Block;
  onPick: (b: Block) => void;
  onClose: () => void;
}) {
  const [c, setC] = useState<ContextItemContent | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const { copied, copy } = useCopy();
  const opaque = block.kind === "other" || block.id.startsWith("seg:");
  useEffect(() => {
    setC(null);
    setErr(null);
    if (block.members || opaque) return;
    let live = true;
    API.contextItem(agent.id, block.id, sessionId)
      .then((x) => live && setC(x))
      .catch((e) => live && setErr(e?.message ?? String(e)));
    return () => {
      live = false;
    };
  }, [agent.id, sessionId, block.id]);
  const total = Math.max(bd.window, bd.used);
  const text = c ? c.parts.map((p) => p.text).join("\n\n") : "";
  return (
    <aside className="cxdetail" aria-label={t("cmap.detail")}>
      <div className="cxdh">
        <i className="dot" style={{ background: blockColor(block.kind) }} />
        <div className="g">
          <b title={itemLabel(block)}>{itemLabel(block)}</b>
          <span>
            {kindLabel(block.kind)}
            {block.group && block.kind !== "messages" ? " · " + block.group : ""}
          </span>
        </div>
        {c && text && (
          <button className="ix" type="button" aria-label={copied ? t("msg.copied") : t("msg.copy")} title={copied ? t("msg.copied") : t("msg.copy")} onClick={() => void copy(text).then((ok) => ok || toast(t("msg.copyFailed")))}>
            {copied ? <Check size={14} /> : <Copy size={14} />}
          </button>
        )}
        <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
          <X size={14} />
        </button>
      </div>
      <div className="cxdm">{t("cmap.pctOf", { tokens: block.tokens.toLocaleString(), pct: pctOf(block.tokens, total) })}</div>
      <div className="cxdb">
        {block.kind === "other" && <p className="small">{t(bd.used > 0 && block.tokens > bd.used * 0.3 ? "cmap.otherNoteBig" : "cmap.otherNote")}</p>}
        {block.members && (
          <div className="cxmembers">
            {block.members.map((m) => (
              <button key={m.id} type="button" className="cxrow sm" onClick={() => onPick(m)}>
                <span className="g">
                  <span className="n">{itemLabel(m)}</span>
                  {m.group && m.kind !== "messages" && <span className="k">{m.group}</span>}
                </span>
                <b>{kfmt(m.tokens)}</b>
              </button>
            ))}
          </div>
        )}
        {!c && !err && !block.members && !opaque && (
          <div className="cxempty">
            <span className="spin sm" /> {t("common.loading")}
          </div>
        )}
        {err && <p className="small warnline">{t("cmap.failed", { error: err })}</p>}
        {c?.parts.map((p, i) => (
          <section key={i} className="cxpart">
            <div className="cxpt">{partTitle(p.title)}</div>
            {!p.text ? (
              <p className="small muted">{t("cmap.empty")}</p>
            ) : p.format === "markdown" ? (
              <div className="cxmd">
                <Markdown text={p.text} />
              </div>
            ) : (
              <pre className="codeblk cxpre">{p.text}</pre>
            )}
          </section>
        ))}
        {c?.truncated && <p className="small warnline">{t("cmap.truncated")}</p>}
      </div>
    </aside>
  );
}

function partTitle(k: string): string {
  const key = `cmap.part.${k}` as Key;
  const v = t(key);
  return v === key ? k : v;
}
