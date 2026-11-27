// 模型擂台 (features/arena): one task sent to several models at once.
//  - ArenaBar: the composer's arena mode, opened in place above the box
//    (/arena, or 擂台 in a 模型圆桌 channel): which 2–4 models answer, and
//    the model this person usually picks for this kind of task
//  - ArenaCard: the arena in the conversation, a column per model (stacked
//    when narrow) with its live answer, then its time, tokens and cost and
//    选这个; the picked answer goes on as the conversation's reply

import { useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Check, RotateCcw, ChevronDown, ChevronUp, Clock, Lightbulb, Plus, Search, Square, Swords, X } from "lucide-react";
import { ArenaAPI, parseRef, refString, type AgentView, type ArenaSuggestion, type ChannelView, type ModelRef, type ModelsResponse } from "../../api";
import { t, type Key } from "../../i18n";
import { allModels, findModel, costfmt, modelOff, providerName, sameRef, shortModel, tokfmt } from "../../format";
import { loadSession, openSubRun, toast, useStore } from "../../store";
import type { ArenaItem, ArenaRun } from "../../projection";
import { modelMark, VendorTile } from "../Avatar";
import { makersOf, type Maker } from "../Roundtable";
import { Markdown } from "../Markdown";
import { StreamMarkdown } from "../StreamMarkdown";
import { ApprovalCard } from "./approval";
import { SessionCtx } from "./context";

export const ARENA_MIN = 2;
export const ARENA_MAX = 4;

const CATS = ["code", "writing", "analysis", "translate", "qa", "other"] as const;
/** A task category's name (写代码 / 写作 / …). */
export function catName(c: string): string {
  return t(`arena.cat.${(CATS as readonly string[]).includes(c) ? c : "other"}` as Key);
}

const plain = (r: ModelRef) => refString(r, false);
const KEY = "crewbase.arenaModels";

function remembered(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) || "[]");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string") : [];
  } catch {
    return [];
  }
}

/** Keeps the models of the last arena for the next one. */
export function rememberArena(refs: string[]) {
  try {
    localStorage.setItem(KEY, JSON.stringify(refs));
  } catch {
    /* private window: the next arena starts from the defaults */
  }
}

const usable = (models: ModelsResponse | null, s: string) => {
  const r = parseRef(s);
  return !!r && !modelOff(models, r) && allModels(models).some((m) => sameRef(m.ref, r));
};

// a maker's mid or fast tier (good value) and its flagships, by model id
const VALUE_TIER = /sonnet|haiku|mini|luna|flash|lite|nano|air|turbo|small|fast/i;
const FLAGSHIP = /opus|fable|astra|ultra|\bmax\b|-pro\b|\bpro\b/i;
/** Output price ($ per million tokens) a default pick stays under. */
const VALUE_OUTPUT_USD = 12;

/** The good-value model of a maker: within the price line when prices
 *  are known (the mid or fast tier first, the best of those), else its
 *  cheapest priced one; without prices the mid or fast tier by name, then
 *  anything that is not a flagship, then whatever it has. */
export function valueModel(models: ModelsResponse | null, mk: Maker): ModelRef {
  const all = mk.models.map((m) => ({ ref: m.ref, out: findModel(models, m.ref)?.pricing?.output }));
  const priced = all.filter((m) => (m.out ?? 0) > 0);
  if (priced.length) {
    const under = priced.filter((m) => m.out! <= VALUE_OUTPUT_USD);
    if (under.length) {
      const tier = under.filter((m) => VALUE_TIER.test(m.ref.model));
      // within the line, the strongest (dearest) of the mid tier
      return (tier.length ? tier : under).reduce((a, b) => (b.out! > a.out! ? b : a)).ref;
    }
    return priced.reduce((a, b) => (b.out! < a.out! ? b : a)).ref;
  }
  return (all.find((m) => VALUE_TIER.test(m.ref.model)) ?? all.find((m) => !FLAGSHIP.test(m.ref.model)) ?? all[0]).ref;
}

/** A 模型圆桌: a channel whose members each wear their model's mark. */
export function isRoundtable(channel: ChannelView | undefined, agents: Record<string, AgentView>): boolean {
  if (!channel || channel.members.length < 2) return false;
  return channel.members.every((id) => agents[id]?.avatar === "vendor" && (agents[id]?.models?.length ?? 0) > 0);
}

/** The models an arena starts with: a roundtable's members' models; else
 *  the last arena's; else the model in use and the strongest of other makers. */
export function defaultArena(models: ModelsResponse | null, current: ModelRef | null, channel: ChannelView | undefined, agents: Record<string, AgentView>): string[] {
  const out: string[] = [];
  const add = (s: string) => {
    if (out.length < ARENA_MAX && !out.includes(s) && usable(models, s)) out.push(s);
  };
  if (channel) for (const id of channel.members) if (agents[id]?.models?.[0]) add(plain(agents[id].models![0]));
  if (out.length >= ARENA_MIN) return out;
  out.length = 0;
  for (const s of remembered()) add(s);
  if (out.length >= ARENA_MIN) return out;
  out.length = 0;
  // one good-value model per maker, the maker of the model in use first
  const mine = (mk: Maker) => !!current && mk.models.some((m) => sameRef(m.ref, current));
  for (const mk of makersOf(models).sort((a, b) => Number(mine(b)) - Number(mine(a)))) {
    if (out.length >= 3) break;
    add(plain(valueModel(models, mk)));
  }
  return out;
}

/** The suggestion for the task being typed (debounced). */
function useSuggestion(text: string): ArenaSuggestion | null {
  const [s, setS] = useState<ArenaSuggestion | null>(null);
  useEffect(() => {
    const q = text.trim();
    if (!q) {
      setS(null);
      return;
    }
    let gone = false;
    const id = setTimeout(() => {
      ArenaAPI.suggest(q).then(
        (r) => !gone && setS(r),
        () => {},
      );
    }, 350);
    return () => {
      gone = true;
      clearTimeout(id);
    };
  }, [text]);
  return s;
}

/** The arena mode above the composer's box: models to pick and the suggestion. */
export function ArenaBar({ picked, setPicked, draft, onClose }: { picked: string[]; setPicked: (p: string[]) => void; draft: string; onClose: () => void }) {
  const models = useStore((s) => s.models);
  const [more, setMore] = useState(false);
  const [q, setQ] = useState("");
  const sugg = useSuggestion(draft);
  const suggested = sugg?.model && usable(models, sugg.model) ? sugg.model : undefined;
  // the short list: what is picked, the suggestion, then one per maker
  const short = useMemo(() => {
    const out: string[] = [...picked];
    if (suggested && !out.includes(suggested)) out.push(suggested);
    for (const mk of makersOf(models)) {
      if (out.length >= 7) break;
      const s = plain(valueModel(models, mk));
      if (!out.includes(s)) out.push(s);
    }
    return out;
  }, [picked, suggested, models]);
  const all = useMemo(() => allModels(models).filter((m) => !modelOff(models, m.ref)), [models]);
  const needle = q.trim().toLowerCase();
  const listed = all.filter((m) => !needle || [m.info.id, m.info.name, m.provider.id].some((x) => x?.toLowerCase().includes(needle)));
  const full = picked.length >= ARENA_MAX;
  const toggle = (s: string) => {
    if (picked.includes(s)) setPicked(picked.filter((x) => x !== s));
    else if (!full) setPicked([...picked, s]);
    else toast(t("arena.max", { n: ARENA_MAX }));
  };
  const name = (s: string) => {
    const r = parseRef(s);
    return r ? shortModel(models, r) : s;
  };
  const chip = (s: string) => {
    const r = parseRef(s);
    const on = picked.includes(s);
    return (
      <button key={s} type="button" className={"archip" + (on ? " on" : "")} aria-pressed={on} disabled={!on && full} onClick={() => toggle(s)} title={s}>
        <VendorTile icon={modelMark(r)} size={18} letter={r ? providerName(r.provider).charAt(0) : "?"} />
        <span className="archn">{name(s)}</span>
        {s === suggested && <span className="arusual">{t("arena.usual")}</span>}
        {on && <Check size={12} className="archk" />}
      </button>
    );
  };
  return (
    <div className="arbar" role="group" aria-label={t("arena.title")}>
      <div className="arbh">
        <Swords size={14} className="aric" />
        <b>{t("arena.title")}</b>
        <span className="arbsub">{t("arena.pickN", { min: ARENA_MIN, max: ARENA_MAX })}</span>
        <span className={"arbn" + (picked.length < ARENA_MIN ? " low" : "")}>
          {picked.length}/{ARENA_MAX}
        </span>
        <button type="button" className="ix arbx" aria-label={t("arena.close")} title={t("arena.close")} onClick={onClose}>
          <X size={14} />
        </button>
      </div>
      <div className="archips">
        {short.map(chip)}
        {all.length > short.length && (
          <button type="button" className="archip more" aria-expanded={more} onClick={() => setMore(!more)}>
            {more ? <ChevronUp size={13} /> : <Plus size={13} />}
            <span className="archn">{more ? t("arena.fewer") : t("arena.more", { n: all.length })}</span>
          </button>
        )}
      </div>
      {more && (
        <div className="arall">
          <label className="arsearch">
            <Search size={13} />
            <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("comp.modelSearch")} aria-label={t("comp.modelSearch")} />
          </label>
          <div className="arlist" role="listbox" aria-multiselectable="true" aria-label={t("arena.allModels")}>
            {listed.map((m) => {
              const s = plain(m.ref);
              const on = picked.includes(s);
              return (
                <button key={s} type="button" role="option" aria-selected={on} className={"arrow" + (on ? " on" : "")} disabled={!on && full} onClick={() => toggle(s)}>
                  <VendorTile icon={modelMark(m.ref)} size={20} letter={providerName(m.ref.provider).charAt(0)} />
                  <span className="g">
                    <b>{m.info.name || m.info.id}</b>
                    <span>{m.provider.id}</span>
                  </span>
                  <span className={"arbox" + (on ? " on" : "")} aria-hidden>
                    {on && <Check size={11} />}
                  </span>
                </button>
              );
            })}
            {!listed.length && <div className="small muted arnone">{t("arena.noMatch")}</div>}
          </div>
        </div>
      )}
      {sugg && suggested && (
        <div className="arsugg">
          <Lightbulb size={13} />
          <span>{t("arena.suggest", { cat: catName(sugg.category), model: name(suggested), wins: sugg.wins ?? 0, n: sugg.picked ?? 0 })}</span>
          {!picked.includes(suggested) && (
            <button type="button" className="linkbtn" disabled={full} onClick={() => toggle(suggested)}>
              {t("arena.addIt")}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- the card

function secs(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  return s < 60 ? `${s.toFixed(s < 10 ? 1 : 0)}s` : `${Math.floor(s / 60)}m${String(Math.round(s % 60)).padStart(2, "0")}s`;
}

/** The last thing a run said: its final answer, the reply streaming in, or
 *  the latest message it wrote between tool calls. */
function useLive(run: ArenaRun): { text: string; streaming: boolean; tool?: string } {
  const key = run.session && run.child ? `${run.session}|${run.child}` : "";
  const stream = useStore((s) => (key ? s.streams[key] : undefined));
  const events = useStore((s) => (run.session ? s.events[run.session] : undefined));
  const doing = useStore((s) => (run.child ? s.doing[run.child] : undefined));
  const done = !!run.result;
  useEffect(() => {
    if (run.session && !done) void loadSession(run.session);
  }, [run.session, done]);
  if (run.result) return { text: run.result.answer ?? "", streaming: false };
  if (stream?.text) return { text: stream.text, streaming: true, tool: doing?.tool };
  let last = "";
  for (const e of events ?? []) {
    if (e.type !== "message/assistant" || e.agent !== run.child) continue;
    const txt = ((e.data?.message?.parts ?? []) as { type: string; text?: string }[])
      .filter((p) => p.type === "text" && p.text)
      .map((p) => p.text)
      .join("");
    if (txt) last = txt;
  }
  return { text: last, streaming: false, tool: doing?.tool };
}

const FOLD_PX = 340;
/** a run with no result and no sub-agent this long after it started was cut off */
const STALE_MS = 30_000;

function Column({ it, run, i, badges, onPick, picking, onRerun }: { it: ArenaItem; run: ArenaRun; i: number; badges: string[]; onPick: () => void; picking: boolean; onRerun?: () => void }) {
  const models = useStore((s) => s.models);
  const agents = useStore((s) => s.agents);
  const approvals = useStore((s) => s.approvals);
  const live = useLive(run);
  const [open, setOpen] = useState(false);
  const [tall, setTall] = useState(false);
  const body = useRef<HTMLDivElement>(null);
  const res = run.result;
  const running = !res;
  const won = it.picked === i;
  const decided = it.picked !== undefined;
  // a folded answer: while it streams, the newest lines show; once done, the start
  useLayoutEffect(() => {
    const el = body.current;
    if (!el) return;
    setTall(el.scrollHeight > FOLD_PX + 40);
    if (!open) el.scrollTop = running ? el.scrollHeight : 0;
  });
  const member = it.members?.[i] ? agents[it.members[i]] : undefined;
  const asks = run.session ? approvals.filter((a) => a.session === run.session) : [];
  const failed = res && res.status !== "completed";
  const [, tick] = useState(0);
  useEffect(() => {
    if (!running || !run.started) return;
    const id = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(id);
  }, [running, run.started]);
  const elapsed = res ? res.ms : run.started ? Date.now() - new Date(run.started).getTime() : 0;
  return (
    <section className={"arcol" + (won ? " won" : decided ? " lost" : "") + (failed ? " bad" : "")} aria-label={shortModel(models, run.model)}>
      <header className="arch">
        <VendorTile icon={modelMark(run.model)} size={26} letter={providerName(run.model.provider).charAt(0)} />
        <span className="g">
          <b title={refString(run.model)}>{shortModel(models, run.model)}</b>
          <span>{member ? member.name : providerName(run.model.provider)}</span>
        </span>
        {won ? (
          <span className="pill ok sm">
            <Check size={11} />
            {t("arena.picked")}
          </span>
        ) : running ? (
          <span className="arlive" aria-label={t("arena.answering")}>
            <i />
            {run.started ? secs(Math.max(0, elapsed)) : t("arena.queued")}
          </span>
        ) : null}
      </header>
      {badges.length > 0 && (
        <div className="arbadges">
          {badges.map((b) => (
            <span key={b} className="arbadge">
              {t(`arena.badge.${b}` as Key)}
            </span>
          ))}
        </div>
      )}
      <div ref={body} className={"arbody" + (open ? " open" : "") + (tall && !open ? " folded" : "") + (running ? " live" : "")} style={open ? undefined : { maxHeight: FOLD_PX }}>
        {failed ? (
          <p className="arerr">
            {res!.status === "cancelled" ? t("arena.stopped") : res!.status === "interrupted" ? t("arena.interrupted") : t("arena.failed")}
            {res!.error && res!.status !== "cancelled" && res!.status !== "interrupted" ? <span className="small" title={res!.error}>{res!.error}</span> : null}
          </p>
        ) : live.text ? (
          live.streaming ? <StreamMarkdown text={live.text} /> : <Markdown text={live.text} />
        ) : (
          <div className="arwait" aria-hidden>
            <i />
            <i />
            <i />
          </div>
        )}
      </div>
      {tall && (
        <button type="button" className="arfold" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
          {open ? t("arena.foldUp") : t("arena.foldOpen")}
        </button>
      )}
      {asks.map((a) => (
        <div key={a.callId} className="arask">
          <ApprovalCard it={{ k: "approval", id: a.callId, req: a, time: a.asked }} agent={agents[a.agent]} />
        </div>
      ))}
      {running && live.tool && !asks.length && (
        <div className="artool small muted">{t("arena.usingTool", { tool: live.tool })}</div>
      )}
      <footer className="arfoot">
        {res && !failed ? (
          <span className="arstats">
            <span title={t("arena.timeTip")}>
              <Clock size={11} />
              {secs(res.ms)}
            </span>
            <span title={t("arena.tokTip", { i: res.input, o: res.output })}>{t("arena.tokens", { i: tokfmt(res.input), o: tokfmt(res.output) })}</span>
            <span className={"arcost" + (res.priced ? "" : " muted")} title={res.priced ? t("arena.costTip") : t("arena.noPriceTip")}>
              {res.priced ? (res.costUsd > 0 ? costfmt(res.costUsd) : t("arena.free")) : t("arena.noPrice")}
            </span>
          </span>
        ) : (
          <span className="sp" />
        )}
        {run.session && (
          <button type="button" className="linkbtn arview" onClick={() => openSubRun(run.session!)}>
            {t("arena.process")}
          </button>
        )}
        {failed && onRerun && (
          <button type="button" className="btn sm arpick" onClick={onRerun}>
            <RotateCcw size={13} />
            {t("arena.rerun")}
          </button>
        )}
        {res && !failed && !decided && (
          <button type="button" className="btn sm arpick" disabled={picking} onClick={onPick}>
            <Check size={13} />
            {t("arena.pick")}
          </button>
        )}
      </footer>
    </section>
  );
}

export function ArenaCard({ it }: { it: ArenaItem }) {
  const session = useContext(SessionCtx);
  const models = useStore((s) => s.models);
  const [picking, setPicking] = useState(false);
  const agents = useStore((s) => s.agents);
  // a run with no result whose sub-agent is gone (or that never started)
  // a while ago was cut off: Crewbase stopped while it ran
  const [, tick] = useState(0);
  const now = Date.now();
  const cutOff = (r: ArenaRun) =>
    !r.result && Object.keys(agents).length > 0 && (r.child ? !agents[r.child] : true) && now - new Date(r.started ?? it.time).getTime() > STALE_MS;
  const runs = it.runs.map((r) => (cutOff(r) ? { ...r, result: { status: "interrupted", ms: 0, calls: 0, input: 0, output: 0, costUsd: 0, priced: true, time: "" } } : r));
  const waiting = it.runs.some((r) => !r.result && !cutOff(r));
  useEffect(() => {
    if (!waiting) return;
    const id = setInterval(() => tick((x) => x + 1), 5000);
    return () => clearInterval(id);
  }, [waiting]);
  const n = runs.length;
  const done = runs.filter((r) => r.result).length;
  const running = done < n;
  const ok = runs.map((r, i) => ({ r, i })).filter((x) => x.r.result?.status === "completed");
  // the fastest and the cheapest of the answers, once there are two to compare
  const badges = (i: number): string[] => {
    if (ok.length < 2 || running) return [];
    const out: string[] = [];
    const fast = ok.reduce((a, b) => (b.r.result!.ms < a.r.result!.ms ? b : a));
    if (fast.i === i) out.push("fast");
    if (ok.every((x) => x.r.result!.priced)) {
      const cheap = ok.reduce((a, b) => (b.r.result!.costUsd < a.r.result!.costUsd ? b : a));
      if (cheap.i === i && ok.some((x) => x.r.result!.costUsd !== cheap.r.result!.costUsd)) out.push("cheap");
    }
    return out;
  };
  const pick = async (i: number) => {
    if (!session) return;
    setPicking(true);
    try {
      await ArenaAPI.pick(session, it.arena, i);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setPicking(false);
    }
  };
  const stop = () => {
    if (session) ArenaAPI.stop(session, it.arena).catch((e) => toast(e?.message ?? String(e)));
  };
  const winner = it.picked !== undefined ? runs[it.picked] : undefined;
  const rerun = (i: number) => {
    if (session) ArenaAPI.rerun(session, it.arena, i).catch((e) => toast(e?.message ?? String(e)));
  };
  return (
    <div className="arena" data-n={n}>
      <div className="arhead">
        <span className="arhic">
          <Swords size={14} />
        </span>
        <b>{t("arena.title")}</b>
        <span className="pill sm">{catName(it.category)}</span>
        <span className="sp" />
        {running ? (
          <>
            <span className="small muted">{t("arena.progress", { done, n })}</span>
            <button type="button" className="btn ghost sm arstop" onClick={stop}>
              <Square size={10} fill="currentColor" />
              {t("arena.stop")}
            </button>
          </>
        ) : winner ? (
          <span className="small muted">{t("arena.wonBy", { model: shortModel(models, winner.model) })}</span>
        ) : ok.length ? (
          <span className="small arcta">{t("arena.choose")}</span>
        ) : (
          <span className="small muted">{t("arena.noneOk")}</span>
        )}
      </div>
      <div className="argrid">
        {runs.map((r, i) => (
          <Column key={i} it={it} run={r} i={i} badges={badges(i)} picking={picking} onPick={() => void pick(i)} onRerun={!running && it.picked === undefined && it.runs[i].result ? () => rerun(i) : undefined} />
        ))}
      </div>
      {winner && <p className="arnote small muted">{t("arena.continues", { model: shortModel(models, winner.model) })}</p>}
    </div>
  );
}
