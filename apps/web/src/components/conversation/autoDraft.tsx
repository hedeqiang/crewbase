// 从对话生成例行流程: a finished turn (or the conversation's menu) turns
// what the person walked the agent through into a routine (例行巡检) or a
// workflow. A cheap model drafts it on the server (…/automation-draft);
// the draft opens right under the turn as an editable card, and 创建 /
// 先试跑一次 go through the routines and workflows APIs, as in 自动化.

import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { ArrowUpRight, CalendarClock, Check, Hand, MessageSquare, Play, RefreshCw, Repeat, Trash, Workflow as WorkflowIcon, X } from "lucide-react";
import { API, parseRef, type AutoDraft, type Routine, type WfRun, type WfStep, type Workflow } from "../../api";
import { t } from "../../i18n";
import { modeLabel, shortModel } from "../../format";
import { openSession, useStore } from "../../store";
import { AgentSelect, ModeSeg, openAutomation, RoutineRuns, runPill, ScheduleField, scheduleLabel } from "../settings/Automation";
import { type FeedItem } from "../../projection";
import { lastMsg } from "./feedUtil";

// ---------------------------------------------------------------- where a draft is open

/** The draft card of a session hangs under one answer (its item id). */
interface Anchor {
  item: string;
  agent: string;
  upto: number;
}
let anchors: Record<string, Anchor | undefined> = {};
let pending = new Set<string>(); // sessions asked from the menu: anchor on the last finished turn
const subs = new Set<() => void>();
const emit = () => subs.forEach((f) => f());
const subscribe = (f: () => void) => {
  subs.add(f);
  return () => void subs.delete(f);
};

function setAnchor(session: string, a: Anchor | undefined) {
  anchors = { ...anchors, [session]: a };
  emit();
}

/** The conversation's menu: open the draft on its last finished turn. */
export function requestAutoDraft(session: string) {
  pending = new Set(pending).add(session);
  emit();
}

function useAnchor(session: string) {
  return useSyncExternalStore(subscribe, () => anchors[session]);
}

/** Mounted in a conversation: turns a menu request into an anchor on the
 *  last finished answer and brings it into view. */
export function AutoDraftResolver({ session, items }: { session: string; items: FeedItem[] }) {
  const want = useSyncExternalStore(subscribe, () => pending.has(session));
  useEffect(() => {
    if (!want) return;
    for (let i = items.length - 1; i >= 0; i--) {
      const it = items[i];
      const m = it.k === "turn" ? (it.open ? undefined : lastMsg(it)) : it.k === "agent" ? it : undefined;
      if (m && m.stats && !m.superseded) {
        pending = new Set([...pending].filter((s) => s !== session));
        setAnchor(session, { item: m.id, agent: m.agent, upto: Number(m.id) });
        window.setTimeout(() => document.querySelector(`.c2r[data-anchor="${CSS.escape(m.id)}"]`)?.scrollIntoView({ block: "nearest", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" }), 60);
        return;
      }
    }
  }, [want, items, session]);
  return null;
}

/** 「做成例行」 among a finished turn's actions. */
export function AutoDraftBtn({ session, item, agent }: { session: string; item: string; agent: string }) {
  const cur = useAnchor(session);
  const on = cur?.item === item;
  return (
    <button
      type="button"
      className={"ix mact c2r-btn" + (on ? " on" : "")}
      aria-expanded={on}
      title={t("c2r.btnTitle")}
      onClick={() => setAnchor(session, on ? undefined : { item, agent, upto: Number(item) })}
    >
      <Repeat size={14} />
      <span>{t("c2r.btn")}</span>
    </button>
  );
}

/** The card, if this answer holds the session's draft. */
export function AutoDraftSlot({ session, item }: { session: string; item: string }) {
  const a = useAnchor(session);
  if (!a || a.item !== item) return null;
  return <AutoDraftCard key={item} session={session} anchor={a} onClose={() => setAnchor(session, undefined)} />;
}

// ---------------------------------------------------------------- the card

type Scope = "turn" | "all";
type Kind = "routine" | "workflow";
type Made = { kind: "routine"; r: Routine; trial: boolean } | { kind: "workflow"; w: Workflow; run?: string };

const errMsg = (e: any) => e?.message ?? String(e);
const PRESETS = ["daily 09:00", "weekly fri 17:00", "every 1h"];

/** The routine prompt: what each run does, then what counts as quiet. */
function composePrompt(task: string, quiet: string) {
  const q = quiet.trim();
  return task.trim() + (q ? "\n\n" + t("c2r.quietPrompt", { quiet: q }) : "");
}

function AutoDraftCard({ session, anchor, onClose }: { session: string; anchor: Anchor; onClose: () => void }) {
  const models = useStore((s) => s.models);
  const [scope, setScope] = useState<Scope>("all");
  const [nonce, setNonce] = useState(0);
  const [draft, setDraft] = useState<AutoDraft | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [kind, setKind] = useState<Kind>("routine");
  const [r, setR] = useState<Routine | null>(null);
  const [task, setTask] = useState("");
  const [quiet, setQuiet] = useState("");
  const [w, setW] = useState<Workflow | null>(null);
  const [busy, setBusy] = useState<"" | "create" | "trial">("");
  // opened below the fold (the answer's end is where the button is): its
  // top comes into view now, and again once the draft has grown it
  const cardRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const id = window.setTimeout(() => cardRef.current?.scrollIntoView({ block: "start", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" }), 40);
    return () => window.clearTimeout(id);
  }, [!!draft]);
  const [made, setMade] = useState<Made | null>(null);

  useEffect(() => {
    let stop = false;
    setDraft(null);
    setErr(null);
    API.automationDraft(session, { agent: anchor.agent, upto: anchor.upto, scope }).then(
      (d) => {
        if (stop) return;
        setDraft(d);
        setKind(d.kind === "workflow" ? "workflow" : "routine");
        setR(d.routine);
        setTask(d.task || d.routine.prompt);
        setQuiet(d.quiet || "");
        setW(d.workflow);
      },
      (e) => !stop && setErr(errMsg(e)),
    );
    return () => {
      stop = true;
    };
  }, [session, anchor.agent, anchor.upto, scope, nonce]);

  const problem = useMemo(() => {
    if (kind === "routine") {
      if (!r) return null;
      if (!r.name.trim()) return t("c2r.needName");
      if (!r.agent) return t("c2r.needAgent");
      if (!task.trim()) return t("c2r.needTask");
      if (!r.schedule.trim()) return t("c2r.needSchedule");
      return null;
    }
    if (!w) return null;
    if (!w.name.trim()) return t("c2r.needName");
    const i = w.steps.findIndex((s) => s.kind !== "approval" && (!s.agent || !s.prompt?.trim()));
    if (i >= 0) return t("c2r.needStep", { n: i + 1 });
    if (!w.steps.some((s) => s.kind !== "approval")) return t("c2r.needAgentStep");
    return null;
  }, [kind, r, w, task]);

  const create = async (trial: boolean) => {
    if (problem || busy) return;
    setBusy(trial ? "trial" : "create");
    setErr(null);
    try {
      if (kind === "routine" && r) {
        const out = await API.createRoutine({ name: r.name.trim(), agent: r.agent, prompt: composePrompt(task, quiet), schedule: r.schedule.trim(), mode: r.mode, origin: session });
        if (trial) await API.runRoutine(out.id);
        setMade({ kind: "routine", r: out, trial });
      } else if (w) {
        const out = await API.createWorkflow({ ...w, name: w.name.trim(), steps: w.steps.map((s) => (s.kind === "approval" ? { id: s.id, name: s.name, kind: s.kind, approval: s.approval } : { id: s.id, name: s.name, kind: "agent", agent: s.agent, prompt: s.prompt, mode: s.mode })) });
        let run: string | undefined;
        if (trial) run = (await API.runWorkflow(out.id, "")).id;
        setMade({ kind: "workflow", w: out, run });
      }
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy("");
    }
  };

  const ref = draft?.model ? parseRef(draft.model) : null;
  const by = ref ? shortModel(models, ref) : "";

  if (made) return <MadeCard made={made} anchor={anchor.item} onClose={onClose} />;

  return (
    <section className="c2r" ref={cardRef} data-anchor={anchor.item} aria-label={t("c2r.title")}>
      <header className="c2r-h">
        <span className="c2r-i" aria-hidden>
          {kind === "workflow" ? <WorkflowIcon size={15} /> : <CalendarClock size={15} />}
        </span>
        <div className="g">
          <b>{t("c2r.title")}</b>
          <span className="small">{t("c2r.sub")}</span>
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
        {draft && (draft.both || kind !== draft.kind) && (
          <div className="seg" role="group" aria-label={t("c2r.kind")}>
            {(["routine", "workflow"] as const).map((k) => (
              <button key={k} type="button" aria-pressed={kind === k} onClick={() => setKind(k)}>
                {t(k === "routine" ? "auto.routines" : "auto.workflows")}
              </button>
            ))}
          </div>
        )}
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
          <span>{t("c2r.reading")}</span>
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

      {draft && kind === "routine" && r && (
        <div className="fgrid c2r-grid">
          <label className="fl" htmlFor="ad-name">
            {t("auto.name")}
          </label>
          <input id="ad-name" className="fi sm" value={r.name} placeholder={t("auto.namePh")} onChange={(e) => setR({ ...r, name: e.target.value })} />
          <label className="fl">{t("auto.agent")}</label>
          <AgentSelect value={r.agent} onChange={(v) => setR({ ...r, agent: v })} />
          <label className="fl" htmlFor="ad-task">
            {t("c2r.task")}
          </label>
          <div>
            <textarea id="ad-task" className="fi sm" rows={Math.max(4, promptRows(task))} value={task} onChange={(e) => setTask(e.target.value)} />
            {!!draft.tools?.length && (
              <div className="c2r-tools small">
                <span>{t("c2r.tools")}</span>
                {draft.tools.map((x) => (
                  <code key={x}>{x}</code>
                ))}
              </div>
            )}
          </div>
          <label className="fl" htmlFor="ad-quiet">
            {t("c2r.quiet")}
          </label>
          <div>
            <input id="ad-quiet" className="fi sm" value={quiet} placeholder={t("c2r.quietPh")} onChange={(e) => setQuiet(e.target.value)} />
            <span className="small">{t("c2r.quietHint")}</span>
          </div>
          <label className="fl">{t("auto.schedule")}</label>
          <div>
            <ScheduleField value={r.schedule} onChange={(v) => setR({ ...r, schedule: v })} presets={uniq([draft.routine.schedule, ...PRESETS])} hint={scheduleNote(draft, r.schedule)} />
          </div>
          <label className="fl">{t("na.mode")}</label>
          <div>
            <ModeSeg value={r.mode || "read-only"} onChange={(v) => setR({ ...r, mode: v })} />
            <div className="small" style={{ marginTop: 4 }}>
              {t(r.mode === "sandbox" ? "c2r.modeSandbox" : "c2r.modeRead")}
            </div>
          </div>
        </div>
      )}

      {draft && kind === "workflow" && w && (
        <div className="fgrid c2r-grid">
          <label className="fl" htmlFor="ad-wname">
            {t("auto.name")}
          </label>
          <input id="ad-wname" className="fi sm" value={w.name} placeholder={t("auto.wfNamePh")} onChange={(e) => setW({ ...w, name: e.target.value })} />
          <label className="fl">{t("auto.trigger")}</label>
          <ScheduleField value={w.trigger} onChange={(v) => setW({ ...w, trigger: v })} presets={uniq([draft.workflow.trigger, ...PRESETS, "manual"])} hint={scheduleNote(draft, w.trigger)} />
          <label className="fl">{t("auto.steps")}</label>
          <ol className="c2r-steps">
            {w.steps.map((s, i) => (
              <StepEdit
                key={s.id}
                n={i + 1}
                s={s}
                last={i === w.steps.length - 1}
                onChange={(x) => setW({ ...w, steps: w.steps.map((y, j) => (j === i ? x : y)) })}
                onRemove={w.steps.length > 1 ? () => setW({ ...w, steps: w.steps.filter((_, j) => j !== i) }) : undefined}
              />
            ))}
          </ol>
        </div>
      )}

      {draft && (
        <footer className="c2r-foot">
          <button className="btn pri" type="button" disabled={!!problem || !!busy} onClick={() => void create(false)}>
            <Check size={13} />
            {busy === "create" ? t("c2r.creating") : t("auto.create")}
          </button>
          <button className="btn" type="button" disabled={!!problem || !!busy} title={t("c2r.trialTitle")} onClick={() => void create(true)}>
            <Play size={12} />
            {busy === "trial" ? t("c2r.creating") : t("c2r.trial")}
          </button>
          {draft.both === false && kind === draft.kind && (
            <button className="btn ghost sm" type="button" onClick={() => setKind(kind === "routine" ? "workflow" : "routine")}>
              {t(kind === "routine" ? "c2r.asWorkflow" : "c2r.asRoutine")}
            </button>
          )}
          <span className="sp" />
          <span className={"small " + (problem || err ? "errt" : "muted")}>{problem || err || (by ? t("c2r.by", { model: by }) : "")}</span>
        </footer>
      )}
    </section>
  );
}

/** Rows for a prompt: its lines, long ones wrapping (about 56 characters a line). */
const promptRows = (s: string) => Math.min(9, Math.max(3, s.split("\n").reduce((n, l) => n + Math.max(1, Math.ceil(l.length / 56)), 0)));
const uniq = (xs: string[]) => [...new Set(xs.filter(Boolean))];

function scheduleNote(d: AutoDraft, cur: string) {
  const proposed = d.kind === "workflow" ? d.workflow.trigger : d.routine.schedule;
  if (cur !== proposed && cur !== d.routine.schedule) return undefined;
  if (d.scheduleGuessed) return t("c2r.schedGuessed", { s: scheduleLabel(proposed) });
  if (d.scheduleWhy) return t("c2r.schedWhy", { why: d.scheduleWhy });
  return undefined;
}

function StepEdit({ n, s, last, onChange, onRemove }: { n: number; s: WfStep; last: boolean; onChange: (s: WfStep) => void; onRemove?: () => void }) {
  const gate = s.kind === "approval";
  return (
    <li className={"c2r-step" + (gate ? " gate" : "") + (last ? " last" : "")}>
      <span className="c2r-n" aria-hidden>
        {gate ? <Hand size={12} /> : n}
      </span>
      <div className="c2r-sb">
        <div className="c2r-sh">
          <input className="fi sm" value={s.name ?? ""} aria-label={t("c2r.stepName", { n })} placeholder={gate ? t("auto.kind.approval") : t("c2r.stepNamePh", { n })} onChange={(e) => onChange({ ...s, name: e.target.value })} />
          {!gate && <AgentSelect value={s.agent ?? ""} onChange={(v) => onChange({ ...s, agent: v })} />}
          {gate && <span className="pill amber">{t("auto.kind.approval")}</span>}
          {onRemove && (
            <button className="ix" type="button" aria-label={t("c2r.removeStep", { n })} title={t("c2r.removeStep", { n })} onClick={onRemove}>
              <Trash size={13} />
            </button>
          )}
        </div>
        {gate ? (
          <textarea className="fi sm" rows={2} value={s.approval?.summary ?? ""} aria-label={t("c2r.gateSummary")} onChange={(e) => onChange({ ...s, approval: { summary: e.target.value } })} />
        ) : (
          <textarea className="fi sm" rows={promptRows(s.prompt ?? "")} value={s.prompt ?? ""} aria-label={t("c2r.stepPrompt", { n })} onChange={(e) => onChange({ ...s, prompt: e.target.value })} />
        )}
        {!gate && <span className="small muted">{modeLabel(s.mode || "sandbox")}</span>}
      </div>
    </li>
  );
}

// ---------------------------------------------------------------- after 创建

function MadeCard({ made, anchor, onClose }: { made: Made; anchor: string; onClose: () => void }) {
  const agents = useStore((s) => s.agents);
  const name = made.kind === "routine" ? made.r.name : made.w.name;
  const when = made.kind === "routine" ? made.r.schedule : made.w.trigger;
  const who = made.kind === "routine" ? agents[made.r.agent]?.name ?? made.r.agent : t("auto.stepsN", { n: made.w.steps.length });
  return (
    <section className="c2r done" data-anchor={anchor} aria-label={t("c2r.title")}>
      <header className="c2r-h">
        <span className="c2r-i ok" aria-hidden>
          <Check size={15} />
        </span>
        <div className="g">
          <b>{t(made.kind === "routine" ? "c2r.madeRoutine" : "c2r.madeWorkflow", { name })}</b>
          <span className="small">
            {scheduleLabel(when)} · {who}
            {made.kind === "routine" ? " · " + modeLabel(made.r.mode) : ""}
          </span>
        </div>
        <button className="ix" type="button" aria-label={t("common.close")} title={t("common.close")} onClick={onClose}>
          <X size={14} />
        </button>
      </header>
      {made.kind === "routine" && made.trial && (
        <div className="c2r-trial">
          <span className="small">{t("c2r.trialRuns")}</span>
          <RoutineRuns r={made.r} />
        </div>
      )}
      {made.kind === "workflow" && made.run && <WfTrial run={made.run} steps={made.w.steps} />}
      <footer className="c2r-foot">
        <button className="btn" type="button" onClick={() => openAutomation(made.kind === "routine" ? "routines" : "workflows", made.kind === "routine" ? made.r.id : made.w.id)}>
          <ArrowUpRight size={13} />
          {t("c2r.openAuto")}
        </button>
        {made.kind === "routine" && made.r.session && (
          <button className="btn ghost" type="button" onClick={() => openSession(made.r.session)}>
            <MessageSquare size={13} />
            {t("c2r.openRuns")}
          </button>
        )}
        {made.kind === "workflow" && made.run && (
          <button className="btn ghost" type="button" onClick={() => openSession(made.run!)}>
            <MessageSquare size={13} />
            {t("c2r.openRun")}
          </button>
        )}
      </footer>
    </section>
  );
}

/** The trial run of a new workflow, step by step, until it ends. */
function WfTrial({ run, steps }: { run: string; steps: WfStep[] }) {
  const [rv, setRv] = useState<WfRun | null>(null);
  useEffect(() => {
    let stop = false;
    let iv = 0;
    const load = () =>
      API.workflowRun(run).then(
        (x) => {
          if (stop) return;
          setRv(x);
          if (x.status !== "running" && x.status !== "waiting") window.clearInterval(iv);
        },
        () => {},
      );
    void load();
    iv = window.setInterval(load, 3000);
    return () => {
      stop = true;
      window.clearInterval(iv);
    };
  }, [run]);
  return (
    <div className="c2r-trial">
      <span className="small">
        {t("c2r.trialRun")} {rv && runPill(rv.status)}
      </span>
      {rv && (
        <div className="runs">
          {rv.steps
            .filter((s) => !s.group)
            .map((s) => (
              <div className="runrow" key={s.id}>
                {runPill(s.status)}
                <span className="small tm">{steps.find((x) => x.id === s.id)?.name || s.id}</span>
                <span className="g small">{s.error || (s.output ? s.output.slice(0, 160) : "")}</span>
              </div>
            ))}
        </div>
      )}
    </div>
  );
}
