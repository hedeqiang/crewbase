// 设置 › 自动化 › 工作流: one page per workflow, edited where it is shown.
// The steps run top to bottom as cards; a card opens in place to edit it,
// "+" between cards inserts a step, the grip drags a card to another gap
// (or onto an agent card, to run alongside it). Changes collect in a draft
// and a bar at the bottom saves them (⌘S). The latest run is drawn onto
// the cards while it is going.

import { Fragment, useEffect, useMemo, useRef, useState, type DragEvent, type ReactNode } from "react";
import {
  ArrowDown,
  ArrowUp,
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  Copy,
  Flag,
  GripVertical,
  Hand,
  Play,
  Plus,
  RotateCcw,
  Square,
  Trash,
  Users,
  Workflow as WorkflowIcon,
  X,
  Zap,
} from "lucide-react";
import { API, type AgentView, type WfRun, type WfStep, type Workflow } from "../../api";
import { t, type Key } from "../../i18n";
import { ago, hhmm, modeLabel, until } from "../../format";
import { getState, toast, useStore } from "../../store";
import { Avatar } from "../Avatar";
import { Markdown } from "../Markdown";
import { EmptyState } from "../EmptyState";
import { Skeleton } from "../Skeleton";
import { Popover, useMenu } from "../Popover";
import { confirmDialog } from "../Confirm";
import { addBranch, insertStep, moveNode, nodeAt, removeNode, type NodeRef } from "./wfsteps";
import { ModeSeg, runPill, scheduleLabel } from "./Automation";

const errMsg = (e: any) => e?.message ?? String(e);
const clone = <T,>(x: T): T => JSON.parse(JSON.stringify(x));
const blank = (): Workflow => ({ id: "", name: "", trigger: "manual", steps: [{ id: "step1", kind: "agent", agent: "", prompt: "{{input}}" }] });
const same = (a: NodeRef | null, b: NodeRef | null) => !!a && !!b && a.stage === b.stage && a.branch === b.branch;

/** What is sent to the server: only the fields each kind of step has. */
function clean(w: Workflow): Workflow {
  return {
    ...w,
    name: w.name.trim(),
    description: w.description?.trim() || undefined,
    steps: w.steps.map((s) =>
      s.kind === "approval"
        ? { id: s.id, name: s.name, kind: s.kind, approval: s.approval }
        : s.kind === "parallel"
          ? { id: s.id, name: s.name, kind: s.kind, parallel: (s.parallel ?? []).map((p) => ({ id: p.id, kind: "agent", agent: p.agent, prompt: p.prompt, mode: p.mode || undefined })) }
          : { id: s.id, name: s.name, kind: "agent", agent: s.agent, prompt: s.prompt, mode: s.mode || undefined },
    ),
  };
}
const sig = (w: Workflow) => JSON.stringify({ n: w.name.trim(), d: w.description?.trim() || "", t: w.trigger, s: clean(w).steps });

/** The first thing that keeps a draft from being saved, said plainly, and where. */
function problem(w: Workflow): { msg: string; at?: NodeRef } | null {
  if (!w.name.trim()) return { msg: t("wf.needName") };
  for (let i = 0; i < w.steps.length; i++) {
    const s = w.steps[i];
    const nodes = s.kind === "parallel" ? (s.parallel ?? []).map((b, j) => ({ b, at: { stage: i, branch: j } })) : s.kind === "agent" ? [{ b: s, at: { stage: i } as NodeRef }] : [];
    for (const { b, at } of nodes) {
      if (!b.agent) return { msg: t("wf.needAgent", { n: i + 1 }), at };
      if (!b.prompt?.trim()) return { msg: t("wf.needPrompt", { n: i + 1 }), at };
    }
  }
  return null;
}

// ---------------------------------------------------------------- list

export function Workflows({ focus }: { focus?: string } = {}) {
  const [list, setList] = useState<Workflow[] | null>(null);
  const [sel, setSel] = useState<string | null>(null); // a workflow id, or "new"
  const [err, setErr] = useState<string | null>(null);
  const dirty = useRef(false);
  const load = () =>
    API.workflows().then(
      (r) => {
        setList(r.workflows ?? []);
        return r.workflows ?? [];
      },
      (e) => {
        setErr(errMsg(e));
        return [] as Workflow[];
      },
    );
  useEffect(() => {
    void load().then((l) => {
      const f = focus && l.find((w) => w.id === focus);
      if (f || l[0]) setSel((s) => s ?? (f || l[0]).id);
    });
  }, []);
  const pick = async (id: string | null) => {
    if (id === sel) return;
    if (dirty.current && !(await confirmDialog(t("wf.discardConfirm")))) return;
    dirty.current = false;
    setSel(id);
  };
  const cur = list?.find((w) => w.id === sel) ?? null;

  return (
    <div className="pvwrap wfwrap">
      <div className="pvlist">
        <button className="btn addwf" type="button" onClick={() => void pick("new")}>
          <Plus size={13} />
          {t("auto.newWorkflow")}
        </button>
        {err && <p className="small errt">{err}</p>}
        {!list && <Skeleton />}
        {sel === "new" && (
          <button type="button" className="pvi" aria-current>
            <span className="wfi">
              <WorkflowIcon size={14} />
            </span>
            <span className="g">
              <b>{t("wf.untitled")}</b>
              <span>{t("wf.notSaved")}</span>
            </span>
          </button>
        )}
        {list?.map((w) => (
          <button key={w.id} type="button" className="pvi" aria-current={sel === w.id} onClick={() => void pick(w.id)}>
            <span className="wfi">
              <WorkflowIcon size={14} />
            </span>
            <span className="g">
              <b>{w.name}</b>
              <span>
                {t("auto.stepsN", { n: w.steps.length })} · {scheduleLabel(w.trigger)}
              </span>
            </span>
          </button>
        ))}
      </div>
      <div className="pvd wfpane">
        {sel === "new" ? (
          <WorkflowView
            key="new"
            init={blank()}
            dirtyRef={dirty}
            onSaved={(w) => void load().then(() => setSel(w.id))}
            onClose={() => {
              dirty.current = false;
              setSel(list?.[0]?.id ?? null);
            }}
          />
        ) : cur ? (
          <WorkflowView
            key={cur.id}
            init={cur}
            dirtyRef={dirty}
            onSaved={() => void load()}
            onClose={() => {
              dirty.current = false;
              void load().then((l) => setSel(l[0]?.id ?? null));
            }}
          />
        ) : (
          list && (
            <EmptyState
              icon={<WorkflowIcon size={22} />}
              title={t("auto.noWorkflows")}
              desc={t("wf.emptyDesc")}
              action={
                <button className="btn" type="button" onClick={() => void pick("new")}>
                  <Plus size={13} />
                  {t("auto.newWorkflow")}
                </button>
              }
            />
          )
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- one workflow

function WorkflowView({ init, dirtyRef, onSaved, onClose }: { init: Workflow; dirtyRef: { current: boolean }; onSaved: (w: Workflow) => void; onClose: () => void }) {
  const isNew = !init.id;
  const [base, setBase] = useState(init);
  const [w, setW] = useState<Workflow>(() => clone(init));
  const [open, setOpen] = useState<NodeRef | null>(isNew ? { stage: 0 } : null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<{ msg: string; at?: NodeRef } | null>(null);
  const [secret, setSecret] = useState<string | null>(null);
  const dirty = isNew || sig(w) !== sig(base);
  dirtyRef.current = dirty && !(isNew && sig(w) === sig(blank()));
  // the list reloaded (another tab, a save): take it unless something here is unsaved
  useEffect(() => {
    if (!dirty) {
      setBase(init);
      setW(clone(init));
    }
  }, [init]);

  const set = (steps: WfStep[], select?: NodeRef | null) => {
    setW((x) => ({ ...x, steps }));
    if (select !== undefined) setOpen(select);
    setErr(null);
  };
  const patchNode = (r: NodeRef, p: Partial<WfStep>) =>
    setW((x) => ({
      ...x,
      steps: x.steps.map((s, i) => {
        if (i !== r.stage) return s;
        if (r.branch === undefined) return { ...s, ...p };
        return { ...s, parallel: (s.parallel ?? []).map((b, j) => (j === r.branch ? { ...b, ...p } : b)) };
      }),
    }));

  const save = async (quiet = false): Promise<Workflow | null> => {
    const p = problem(w);
    if (p) {
      setErr(p);
      if (p.at) setOpen(p.at);
      return null;
    }
    setBusy(true);
    setErr(null);
    try {
      const out = isNew ? await API.createWorkflow(clean(w)) : await API.putWorkflow(init.id, clean(w));
      if (!quiet) toast(isNew ? t("auto.created", { name: out.name }) : t("panel.saved"));
      if (out.secret) setSecret(out.secret);
      setBase(out);
      setW(clone(out));
      dirtyRef.current = false;
      onSaved(out);
      return out;
    } catch (e) {
      setErr({ msg: errMsg(e) });
      return null;
    } finally {
      setBusy(false);
    }
  };
  const discard = () => {
    if (isNew) return onClose();
    setW(clone(base));
    setErr(null);
  };
  // ⌘S saves
  const saveRef = useRef(save);
  saveRef.current = save;
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        void saveRef.current();
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);

  const del = async () => {
    if (!(await confirmDialog(t("auto.deleteConfirm", { name: base.name })))) return;
    try {
      await API.deleteWorkflow(base.id);
      onClose();
    } catch (e) {
      toast(errMsg(e));
    }
  };

  // runs
  const [runs, setRuns] = useState<WfRun[] | null>(isNew ? [] : null);
  const [openRun, setOpenRun] = useState<string | null>(null);
  const loadRuns = () => (isNew ? Promise.resolve() : API.workflowRuns(base.id).then((r) => setRuns(r.runs ?? []), () => setRuns([])));
  const live = runs?.find((r) => r.status === "running" || r.status === "waiting");
  useEffect(() => {
    void loadRuns();
  }, [base.id]);
  useEffect(() => {
    if (!live) return;
    const iv = window.setInterval(() => void loadRuns(), 2000);
    return () => window.clearInterval(iv);
  }, [live?.id, base.id]);
  // the run drawn onto the cards: the one going, else the one opened below
  const shown = live ?? runs?.find((r) => r.id === openRun) ?? null;
  const status = useMemo(() => {
    const m: Record<string, string> = {};
    for (const s of shown?.steps ?? []) m[s.id] = s.status;
    return m;
  }, [shown]);

  const run = async (input: string) => {
    let id = base.id;
    if (dirty) {
      const out = await save(true);
      if (!out) return;
      id = out.id;
    }
    try {
      const r = await API.runWorkflow(id, input);
      toast(t("auto.started", { name: w.name }));
      setOpenRun(r.id);
      setRuns((l) => [r, ...(l ?? [])]);
      void loadRuns();
    } catch (e) {
      toast(errMsg(e));
    }
  };

  const usesInput = JSON.stringify(w.steps).includes("{{input}}");
  const hook = base.hook && w.trigger === "webhook" ? `${location.origin}${base.hook}` : "";

  return (
    <div className="wfv">
      <div className="wfv-h">
        <span className="wfv-ico">
          <WorkflowIcon size={18} />
        </span>
        <div className="wfv-t">
          <input
            className="wfv-name"
            value={w.name}
            placeholder={t("wf.namePh")}
            aria-label={t("auto.name")}
            autoFocus={isNew}
            onChange={(e) => {
              setW({ ...w, name: e.target.value });
              setErr(null);
            }}
          />
          <input className="wfv-desc" value={w.description ?? ""} placeholder={t("wf.descPh")} aria-label={t("auto.desc")} onChange={(e) => setW({ ...w, description: e.target.value })} />
        </div>
        <div className="wfv-acts">
          {!isNew && (
            <button className="ix" type="button" aria-label={t("common.delete")} title={t("common.delete")} onClick={del}>
              <Trash size={14} />
            </button>
          )}
          <RunButton usesInput={usesInput} busy={busy || !!live} onRun={run} />
        </div>
      </div>

      <div className="wfv-when">
        <span className="wfv-lbl">
          <Zap size={13} />
          {t("wf.when")}
        </span>
        <TriggerPicker value={w.trigger} onChange={(v) => setW({ ...w, trigger: v })} />
        {!dirty && base.nextRun && <span className="small muted">{t("auto.nextRun", { time: until(base.nextRun) })}</span>}
      </div>
      {hook && (
        <div className="wfv-hook">
          <code className="mono">POST {hook}</code>
          <button className="ix" type="button" aria-label={t("dev.copyLink")} title={t("dev.copyLink")} onClick={() => void navigator.clipboard?.writeText(hook).then(() => toast(t("dev.copied")))}>
            <Copy size={13} />
          </button>
        </div>
      )}
      {secret && (
        <div className="box rulenote warnbox">
          <div style={{ minWidth: 0, flex: 1 }}>
            <b>{t("auto.secretTitle", { name: base.name })}</b>
            <code className="cmd">{secret}</code>
            <span className="small">{t("auto.secretNote")}</span>
          </div>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={() => setSecret(null)}>
            <X size={13} />
          </button>
        </div>
      )}

      <Flow
        w={w}
        open={open}
        setOpen={setOpen}
        set={set}
        patchNode={patchNode}
        status={shown ? status : null}
        bad={err?.at ?? null}
        banner={
          shown && (
            <div className="flow-run">
              {runPill(shown.status)}
              <span>{t("wf.showingRun", { time: hhmm(shown.started) })}</span>
              {!live && (
                <button className="ix" type="button" aria-label={t("common.close")} onClick={() => setOpenRun(null)}>
                  <X size={12} />
                </button>
              )}
            </div>
          )
        }
      />

      {!isNew && (
        <>
          <div className="psec2">{t("auto.runs")}</div>
          {!runs ? (
            <Skeleton />
          ) : runs.length === 0 ? (
            <p className="small muted">{t("wf.noRuns")}</p>
          ) : (
            <div className="wruns">
              {runs.slice(0, 15).map((r) => (
                <RunCard
                  key={r.id}
                  r={r}
                  steps={w.steps.length}
                  open={openRun === r.id}
                  toggle={() => setOpenRun(openRun === r.id ? null : r.id)}
                  onChanged={(x) => {
                    if (x) setOpenRun(x.id);
                    void loadRuns();
                  }}
                />
              ))}
            </div>
          )}
        </>
      )}

      <div className={"savebar" + (dirty ? " show" : "")} aria-hidden={!dirty}>
        <span className={"g" + (err ? " errt" : "")}>{err ? err.msg : isNew ? t("wf.newUnsaved") : t("wf.unsaved")}</span>
        <button className="btn ghost sm" type="button" onClick={discard} tabIndex={dirty ? 0 : -1}>
          {isNew ? t("common.cancel") : t("wf.discard")}
        </button>
        <button className="btn pri sm" type="button" disabled={busy} onClick={() => void save()} tabIndex={dirty ? 0 : -1}>
          {isNew ? t("auto.create") : t("common.save")}
          <kbd>⌘S</kbd>
        </button>
      </div>
    </div>
  );
}

/** 运行: straight away, or first asking for the input when a step uses {{input}}. */
function RunButton({ usesInput, busy, onRun }: { usesInput: boolean; busy: boolean; onRun: (input: string) => Promise<void> }) {
  const m = useMenu();
  const [input, setInput] = useState("");
  const go = () => {
    m.close();
    void onRun(input.trim()).then(() => setInput(""));
  };
  return (
    <>
      <button className="btn pri" type="button" ref={m.ref} disabled={busy} onClick={() => (usesInput ? m.toggle() : go())}>
        <Play size={13} />
        {t("wf.run")}
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} align="end" className="runpop" width={320}>
        <label className="fl">{t("wf.runInput")}</label>
        <textarea
          className="fi sm"
          rows={3}
          autoFocus
          value={input}
          placeholder={t("wf.runInputPh")}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) go();
          }}
        />
        <div className="acts sm">
          <span className="small muted">{t("wf.runInputHint")}</span>
          <span className="sp" />
          <button className="btn pri sm" type="button" onClick={go}>
            <Play size={12} />
            {t("wf.run")}
          </button>
        </div>
      </Popover>
    </>
  );
}

// ---------------------------------------------------------------- when

type When = { kind: "manual" } | { kind: "webhook" } | { kind: "daily"; time: string } | { kind: "weekly"; day: string; time: string } | { kind: "every"; every: string } | { kind: "custom"; raw: string };
const DAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];
const EVERY = ["15m", "30m", "1h", "2h", "3h", "6h", "12h"];
const pad = (s: string) => (s.length === 4 ? "0" + s : s);

function parseWhen(v: string): When {
  const s = v.trim().toLowerCase();
  if (!s || s === "manual") return { kind: "manual" };
  if (s === "webhook") return { kind: "webhook" };
  let m = /^daily (\d\d?:\d\d)$/.exec(s);
  if (m) return { kind: "daily", time: pad(m[1]) };
  m = /^weekly (\w{3})\w* (\d\d?:\d\d)$/.exec(s);
  if (m && DAYS.includes(m[1])) return { kind: "weekly", day: m[1], time: pad(m[2]) };
  m = /^every (\d+[mh])$/.exec(s);
  if (m) return { kind: "every", every: m[1] };
  return { kind: "custom", raw: v };
}
function whenSpec(w: When): string {
  switch (w.kind) {
    case "manual":
      return "manual";
    case "webhook":
      return "webhook";
    case "daily":
      return `daily ${w.time}`;
    case "weekly":
      return `weekly ${w.day} ${w.time}`;
    case "every":
      return `every ${w.every}`;
    case "custom":
      return w.raw;
  }
}
const everyLabel = (e: string) => t("auto.every", { d: e.replace(/h$/, t("auto.hour")).replace(/m$/, t("auto.minute")) });

/** 何时运行: by hand, on a schedule (said in words, not cron), or from a webhook. */
function TriggerPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const w = parseWhen(value);
  const kind = w.kind === "manual" || w.kind === "webhook" ? w.kind : "schedule";
  const set = (x: When) => onChange(whenSpec(x));
  return (
    <div className="trig">
      <div className="seg">
        <button type="button" aria-pressed={kind === "manual"} onClick={() => set({ kind: "manual" })}>
          {t("wf.byHand")}
        </button>
        <button type="button" aria-pressed={kind === "schedule"} onClick={() => kind !== "schedule" && set({ kind: "daily", time: "09:00" })}>
          {t("wf.onSchedule")}
        </button>
        <button type="button" aria-pressed={kind === "webhook"} onClick={() => set({ kind: "webhook" })}>
          Webhook
        </button>
      </div>
      {kind === "schedule" && (
        <div className="trig-s">
          <span className="ssel sm">
            <select
              value={w.kind}
              aria-label={t("wf.often")}
              onChange={(e) => {
                const k = e.target.value;
                const time = "time" in w ? w.time : "09:00";
                if (k === "daily") set({ kind: "daily", time });
                else if (k === "weekly") set({ kind: "weekly", day: "mon", time });
                else if (k === "every") set({ kind: "every", every: "1h" });
                else set({ kind: "custom", raw: "0 9 * * 1-5" });
              }}
            >
              <option value="daily">{t("wf.daily")}</option>
              <option value="weekly">{t("wf.weekly")}</option>
              <option value="every">{t("wf.everyN")}</option>
              <option value="custom">{t("wf.cron")}</option>
            </select>
          </span>
          {w.kind === "weekly" && (
            <span className="ssel sm">
              <select value={w.day} aria-label={t("wf.day")} onChange={(e) => set({ ...w, day: e.target.value })}>
                {DAYS.map((d) => (
                  <option key={d} value={d}>
                    {t(`auto.wd.${d}` as Key)}
                  </option>
                ))}
              </select>
            </span>
          )}
          {(w.kind === "daily" || w.kind === "weekly") && (
            <input className="fi sm trig-time" type="time" value={w.time} aria-label={t("wf.time")} onChange={(e) => e.target.value && set({ ...w, time: e.target.value })} />
          )}
          {w.kind === "every" && (
            <span className="ssel sm">
              <select value={w.every} aria-label={t("wf.interval")} onChange={(e) => set({ kind: "every", every: e.target.value })}>
                {[...new Set([...EVERY, w.every])].map((e) => (
                  <option key={e} value={e}>
                    {everyLabel(e)}
                  </option>
                ))}
              </select>
            </span>
          )}
          {w.kind === "custom" && (
            <input className="fi sm mono trig-cron" value={w.raw} placeholder="0 9 * * 1-5" aria-label={t("wf.cron")} title={t("auto.scheduleHint")} onChange={(e) => set({ kind: "custom", raw: e.target.value })} />
          )}
        </div>
      )}
      {kind === "webhook" && <span className="small muted">{t("wf.webhookHint")}</span>}
    </div>
  );
}

// ---------------------------------------------------------------- flow

const STATUS_ICON: Record<string, ReactNode> = {
  running: <span className="wfring" />,
  completed: <Check size={13} />,
  quiet: <Check size={13} />,
  failed: <X size={13} />,
  rejected: <X size={13} />,
  cancelled: <X size={13} />,
  waiting: <Hand size={12} />,
};

function Flow({
  w,
  open,
  setOpen,
  set,
  patchNode,
  status,
  bad,
  banner,
}: {
  w: Workflow;
  open: NodeRef | null;
  setOpen: (r: NodeRef | null) => void;
  set: (steps: WfStep[], select?: NodeRef | null) => void;
  patchNode: (r: NodeRef, p: Partial<WfStep>) => void;
  status: Record<string, string> | null;
  bad: NodeRef | null;
  banner: ReactNode;
}) {
  const steps = w.steps;
  const [drag, setDrag] = useState<NodeRef | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const dragIsApproval = !!drag && nodeAt(steps, drag)?.kind === "approval";

  const grip = (r: NodeRef) => ({
    draggable: true,
    onDragStart: (e: DragEvent) => {
      setDrag(r);
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", "step");
    },
    onDragEnd: () => {
      setDrag(null);
      setOver(null);
    },
  });
  const zone = (key: string, to: { gap: number } | { join: number }) => ({
    onDragOver: (e: DragEvent) => {
      if (!drag) return;
      e.preventDefault();
      setOver(key);
    },
    onDragLeave: () => setOver((o) => (o === key ? null : o)),
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      if (drag) set(moveNode(steps, drag, to), null);
      setDrag(null);
      setOver(null);
    },
  });
  // a step dropped back where it is is not a move: those gaps stay quiet
  const gapUseful = (at: number) => !drag || drag.branch !== undefined || (at !== drag.stage && at !== drag.stage + 1);

  const moveStage = (i: number, d: -1 | 1) => {
    const j = i + d;
    if (j < 0 || j >= steps.length) return;
    const out = [...steps];
    const [s] = out.splice(i, 1);
    out.splice(j, 0, s);
    set(out, open && open.stage === i ? { ...open, stage: j } : open);
  };
  const toggle = (r: NodeRef) => setOpen(same(open, r) ? null : r);
  const stat = (id: string) => (status ? status[id] ?? "pending" : null);

  return (
    <div className={"flow" + (drag ? " dragging" : "")}>
      {banner}
      <div className="flow-cap">
        <Play size={12} />
        {w.trigger === "webhook" ? t("wf.startHook") : !w.trigger || w.trigger === "manual" ? t("wf.startHand") : scheduleLabel(w.trigger)}
      </div>
      {gapUseful(0) ? <Gap at={0} steps={steps} set={set} over={over === "g0"} drop={zone("g0", { gap: 0 })} live={!!drag} /> : <div className="flow-line" />}
      {steps.map((s, i) => {
        const joinable = s.kind !== "approval" && !!drag && !dragIsApproval && drag.stage !== i;
        const props = { n: i + 1, s, i, steps, open, toggle, set, patchNode, moveStage, grip, stat, bad };
        return (
          <Fragment key={s.id + ":" + i}>
            <div className={"flow-stage" + (over === "j" + i ? " over" : "")} {...(joinable ? zone("j" + i, { join: i }) : {})}>
              {s.kind === "parallel" ? <ParallelCard {...props} /> : s.kind === "approval" ? <ApprovalCard {...props} /> : <AgentCard {...props} r={{ stage: i }} />}
              {joinable && <span className="flow-join">{t("wfc.joinHint")}</span>}
            </div>
            {gapUseful(i + 1) ? <Gap at={i + 1} steps={steps} set={set} over={over === "g" + (i + 1)} drop={zone("g" + (i + 1), { gap: i + 1 })} live={!!drag} /> : <div className="flow-line" />}
          </Fragment>
        );
      })}
      <div className="flow-cap end">
        <Flag size={12} />
        {t("wfc.end")}
      </div>
    </div>
  );
}

/** The line between two steps, with "+" to insert one; a drop target while dragging. */
function Gap({ at, steps, set, over, drop, live }: { at: number; steps: WfStep[]; set: (s: WfStep[], r?: NodeRef | null) => void; over: boolean; drop: object; live: boolean }) {
  const m = useMenu();
  const add = (k: "agent" | "parallel" | "approval") => {
    m.close();
    const r = insertStep(steps, at, k);
    set(r.steps, r.ref);
  };
  return (
    <div className={"flow-gap" + (over ? " over" : "") + (live ? " live" : "") + (m.open ? " on" : "")} {...drop}>
      <span className="flow-line" />
      {live ? (
        <span className="flow-drop">{t("wf.dropHere")}</span>
      ) : (
        <button type="button" className="flow-plus" ref={m.ref} aria-label={t("wfc.insert")} title={t("wfc.insert")} onClick={m.toggle}>
          <Plus size={13} />
        </button>
      )}
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} className="flow-menu" width={240}>
        {(["agent", "parallel", "approval"] as const).map((k) => (
          <button key={k} type="button" className="mi" onClick={() => add(k)}>
            <span className={"flow-mico " + k}>{k === "agent" ? <Bot size={14} /> : k === "parallel" ? <Users size={14} /> : <Hand size={14} />}</span>
            <span className="g">
              <b>{t(`wf.add.${k}` as Key)}</b>
              <span>{t(`wf.add.${k}Desc` as Key)}</span>
            </span>
          </button>
        ))}
      </Popover>
    </div>
  );
}

type CardProps = {
  n: number;
  s: WfStep;
  i: number;
  steps: WfStep[];
  open: NodeRef | null;
  toggle: (r: NodeRef) => void;
  set: (s: WfStep[], r?: NodeRef | null) => void;
  patchNode: (r: NodeRef, p: Partial<WfStep>) => void;
  moveStage: (i: number, d: -1 | 1) => void;
  grip: (r: NodeRef) => object;
  stat: (id: string) => string | null;
  bad: NodeRef | null;
};

/** The number of a step, or how it went in the run shown. */
function Badge({ n, st }: { n: number; st: string | null }) {
  if (!st || st === "pending") return <span className={"fnum" + (st ? " pending" : "")}>{n}</span>;
  return (
    <span className={"fnum st-" + st} title={t(`auto.st.${st}` as Key)}>
      {STATUS_ICON[st] ?? n}
    </span>
  );
}

function AgentCard({ n, s, i, steps, open, toggle, set, patchNode, moveStage, grip, stat, bad, r }: CardProps & { r: NodeRef }) {
  const agents = useStore((x) => x.agents);
  const a = s.agent ? agents[s.agent] : undefined;
  const isOpen = same(open, r);
  return (
    <div className={"fstep" + (isOpen ? " open" : "") + (same(bad, r) ? " bad" : "")}>
      <div className="fstep-h" role="button" tabIndex={0} aria-expanded={isOpen} onClick={() => toggle(r)} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && e.target === e.currentTarget && (e.preventDefault(), toggle(r))}>
        <Badge n={n} st={stat(s.id)} />
        <Who a={a} id={s.agent} />
        {s.mode === "read-only" && <span className="tag">{modeLabel("read-only")}</span>}
        <span className="fgrip" title={t("wf.drag")} {...grip(r)} onClick={(e) => e.stopPropagation()}>
          <GripVertical size={14} />
        </span>
        <ChevronDown size={15} className="fchev" />
      </div>
      {!isOpen && <Prompt text={s.prompt} steps={steps} />}
      {isOpen && (
        <div className="fstep-b">
          <AgentFields s={s} steps={steps} stage={i} onChange={(p) => patchNode(r, p)} />
          <div className="fstep-f">
            <button className="btn ghost sm" type="button" disabled={i === 0} onClick={() => moveStage(i, -1)}>
              <ArrowUp size={12} /> {t("wf.up")}
            </button>
            <button className="btn ghost sm" type="button" disabled={i === steps.length - 1} onClick={() => moveStage(i, 1)}>
              <ArrowDown size={12} /> {t("wf.down")}
            </button>
            <button
              className="btn ghost sm"
              type="button"
              onClick={() => {
                const x = addBranch(steps, i);
                set(x.steps, x.ref);
              }}
            >
              <Users size={12} /> {t("wf.alongside")}
            </button>
            <span className="sp" />
            <StepId s={s} onChange={(id) => patchNode(r, { id })} />
            <button className="ix" type="button" aria-label={t("panel.remove")} title={t("panel.remove")} disabled={steps.length === 1} onClick={() => set(removeNode(steps, r), null)}>
              <Trash size={13} />
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function ParallelCard({ n, s, i, steps, open, toggle, set, patchNode, moveStage, grip, stat, bad }: CardProps) {
  const agents = useStore((x) => x.agents);
  const branches = s.parallel ?? [];
  const cur = open && open.stage === i && open.branch !== undefined ? open.branch : null;
  const b = cur !== null ? branches[cur] : undefined;
  const r = { stage: i };
  return (
    <div className={"fstep par" + (cur !== null ? " open" : "")}>
      <div className="fstep-h static">
        <Badge n={n} st={stat(s.id)} />
        <span className="fwho">
          <span className="fav par">
            <Users size={14} />
          </span>
          <span className="g">
            <b>{t("wf.together", { n: branches.length })}</b>
            <span>{t("wf.togetherDesc")}</span>
          </span>
        </span>
        <span className="fgrip" title={t("wf.drag")} {...grip(r)}>
          <GripVertical size={14} />
        </span>
        <StageMenu i={i} steps={steps} moveStage={moveStage} remove={() => set(removeNode(steps, r), null)} />
      </div>
      <div className="fbr">
        {branches.map((x, j) => {
          const br = { stage: i, branch: j };
          const a = x.agent ? agents[x.agent] : undefined;
          const st = stat(x.id);
          return (
            <div
              key={x.id + j}
              className={"fbr-i" + (cur === j ? " on" : "") + (same(bad, br) ? " bad" : "") + (st ? " st-" + st : "")}
              role="button"
              tabIndex={0}
              aria-pressed={cur === j}
              onClick={() => toggle(br)}
              onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && (e.preventDefault(), toggle(br))}
            >
              <div className="fbr-h">
                <Who a={a} id={x.agent} small />
                {st && st !== "pending" && <span className={"fdot st-" + st}>{STATUS_ICON[st]}</span>}
                <span className="fgrip" title={t("wf.drag")} {...grip(br)} onClick={(e) => e.stopPropagation()}>
                  <GripVertical size={13} />
                </span>
              </div>
              <Prompt text={x.prompt} steps={steps} />
            </div>
          );
        })}
        <button
          type="button"
          className="fbr-add"
          onClick={() => {
            const x = addBranch(steps, i);
            set(x.steps, x.ref);
          }}
        >
          <Plus size={14} />
          {t("wf.addAgent")}
        </button>
      </div>
      {b && cur !== null && (
        <div className="fstep-b">
          <AgentFields s={b} steps={steps} stage={i} onChange={(p) => patchNode({ stage: i, branch: cur }, p)} />
          <div className="fstep-f">
            <button className="btn ghost sm" type="button" onClick={() => set(moveNode(steps, { stage: i, branch: cur }, { gap: i + 1 }), { stage: i + (branches.length > 1 ? 1 : 0) })}>
              <ArrowDown size={12} /> {t("wf.ownStep")}
            </button>
            <span className="sp" />
            <StepId s={b} onChange={(id) => patchNode({ stage: i, branch: cur }, { id })} />
            <button className="ix" type="button" aria-label={t("panel.remove")} title={t("panel.remove")} onClick={() => set(removeNode(steps, { stage: i, branch: cur }), null)}>
              <Trash size={13} />
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function ApprovalCard({ n, s, i, steps, open, toggle, set, patchNode, moveStage, grip, stat }: CardProps) {
  const r = { stage: i };
  const isOpen = same(open, r);
  return (
    <div className={"fstep appr" + (isOpen ? " open" : "")}>
      <div className="fstep-h" role="button" tabIndex={0} aria-expanded={isOpen} onClick={() => toggle(r)} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && e.target === e.currentTarget && (e.preventDefault(), toggle(r))}>
        <Badge n={n} st={stat(s.id)} />
        <span className="fwho">
          <span className="fav appr">
            <Hand size={14} />
          </span>
          <span className="g">
            <b>{t("wf.approval")}</b>
            <span>{s.approval?.summary || t("wfc.approvalDesc")}</span>
          </span>
        </span>
        <span className="fgrip" title={t("wf.drag")} {...grip(r)} onClick={(e) => e.stopPropagation()}>
          <GripVertical size={14} />
        </span>
        <ChevronDown size={15} className="fchev" />
      </div>
      {isOpen && (
        <div className="fstep-b">
          <label className="fl">{t("wf.approvalAsk")}</label>
          <input className="fi sm" autoFocus value={s.approval?.summary ?? ""} placeholder={t("auto.approvalPh")} onChange={(e) => patchNode(r, { approval: { summary: e.target.value } })} />
          <div className="fstep-f">
            <button className="btn ghost sm" type="button" disabled={i === 0} onClick={() => moveStage(i, -1)}>
              <ArrowUp size={12} /> {t("wf.up")}
            </button>
            <button className="btn ghost sm" type="button" disabled={i === steps.length - 1} onClick={() => moveStage(i, 1)}>
              <ArrowDown size={12} /> {t("wf.down")}
            </button>
            <span className="sp" />
            <button className="ix" type="button" aria-label={t("panel.remove")} title={t("panel.remove")} disabled={steps.length === 1} onClick={() => set(removeNode(steps, r), null)}>
              <Trash size={13} />
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

/** ⋯ on a parallel group: move it, or remove the whole group. */
function StageMenu({ i, steps, moveStage, remove }: { i: number; steps: WfStep[]; moveStage: (i: number, d: -1 | 1) => void; remove: () => void }) {
  const m = useMenu();
  return (
    <>
      <button className="ix" type="button" ref={m.ref} aria-label={t("wf.more")} title={t("wf.more")} onClick={m.toggle}>
        <ChevronDown size={15} />
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} align="end" className="flow-menu" width={200}>
        <button type="button" className="mi" disabled={i === 0} onClick={() => (m.close(), moveStage(i, -1))}>
          <ArrowUp size={14} /> {t("wf.up")}
        </button>
        <button type="button" className="mi" disabled={i === steps.length - 1} onClick={() => (m.close(), moveStage(i, 1))}>
          <ArrowDown size={14} /> {t("wf.down")}
        </button>
        <button type="button" className="mi dng" disabled={steps.length === 1} onClick={() => (m.close(), remove())}>
          <Trash size={14} /> {t("wf.removeGroup")}
        </button>
      </Popover>
    </>
  );
}

function Who({ a, id, small }: { a?: AgentView; id?: string; small?: boolean }) {
  return (
    <span className={"fwho" + (small ? " sm" : "")}>
      {a ? (
        <Avatar agent={a} size={small ? 22 : 28} />
      ) : (
        <span className="fav none">
          <Bot size={small ? 12 : 14} />
        </span>
      )}
      <span className="g">
        <b className={a ? "" : "amber-t"}>{a?.name ?? (id || t("wfc.pickAgent"))}</b>
        {!small && a?.role && <span>{a.role}</span>}
      </span>
    </span>
  );
}

/** A prompt as people read it: {{input}} and {{prev}} as small labels. */
function Prompt({ text, steps }: { text?: string; steps: WfStep[] }) {
  if (!text?.trim()) return <div className="fprompt muted">{t("wf.noPrompt")}</div>;
  const parts: ReactNode[] = [];
  const re = /\{\{\s*(input|prev|steps\.([A-Za-z0-9_-]+))\s*\}\}/g;
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    if (m.index > last) parts.push(text.slice(last, m.index));
    parts.push(
      <span key={m.index} className="ftok">
        {m[1] === "input" ? t("wf.tok.input") : m[1] === "prev" ? t("wf.tok.prev") : t("wf.tok.step", { step: stepTitle(steps, m[2]) })}
      </span>,
    );
    last = m.index + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return <div className="fprompt">{parts}</div>;
}

/** A step called by its place and agent ("第 1 步 · Milo"), not its id. */
function stepTitle(steps: WfStep[], id: string) {
  const agents = getState().agents;
  for (let i = 0; i < steps.length; i++) {
    const s = steps[i];
    for (const x of [s, ...(s.parallel ?? [])]) if (x.id === id) return t("wf.stepN", { n: i + 1 }) + (x.agent ? " · " + (agents[x.agent]?.name ?? x.agent) : "");
  }
  return id;
}

function AgentFields({ s, steps, stage, onChange }: { s: WfStep; steps: WfStep[]; stage: number; onChange: (p: Partial<WfStep>) => void }) {
  const ta = useRef<HTMLTextAreaElement>(null);
  const insert = (tok: string) => {
    const el = ta.current;
    const v = s.prompt ?? "";
    const at = el ? el.selectionStart : v.length;
    const end = el ? el.selectionEnd : v.length;
    const pre = v.slice(0, at);
    const sep = pre && !/\s$/.test(pre) ? " " : "";
    onChange({ prompt: pre + sep + tok + v.slice(end) });
    requestAnimationFrame(() => {
      el?.focus();
      const p = at + sep.length + tok.length;
      el?.setSelectionRange(p, p);
    });
  };
  // earlier steps whose result can be used by name
  const earlier = steps.slice(0, stage).flatMap((x) => (x.kind === "parallel" ? x.parallel ?? [] : x.kind === "agent" ? [x] : []));
  const fit = (el: HTMLTextAreaElement | null) => {
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight + 2, 260) + "px";
  };
  useEffect(() => fit(ta.current), [s.prompt]);
  return (
    <div className="ffields">
      <label className="fl">{t("wf.who")}</label>
      <div className="frow">
        <AgentPick value={s.agent ?? ""} onChange={(v) => onChange({ agent: v })} />
        <ModeSeg value={s.mode || "sandbox"} onChange={(v) => onChange({ mode: v })} />
      </div>
      <label className="fl">{t("wf.what")}</label>
      <textarea ref={ta} className="fi sm fta" rows={3} value={s.prompt ?? ""} placeholder={t("wf.whatPh")} onChange={(e) => onChange({ prompt: e.target.value })} />
      <div className="ftoks">
        <span className="small muted">{t("wf.insert")}</span>
        <button type="button" className="ftok add" onClick={() => insert("{{input}}")}>
          <Plus size={11} />
          {t("wf.tok.input")}
        </button>
        {stage > 0 && (
          <button type="button" className="ftok add" onClick={() => insert("{{prev}}")}>
            <Plus size={11} />
            {t("wf.tok.prev")}
          </button>
        )}
        {earlier.length > 1 &&
          earlier.map((x) => (
            <button key={x.id} type="button" className="ftok add" onClick={() => insert(`{{steps.${x.id}}}`)}>
              <Plus size={11} />
              {t("wf.tok.step", { step: stepTitle(steps, x.id) })}
            </button>
          ))}
      </div>
    </div>
  );
}

/** 交给谁: the agents with their faces. */
function AgentPick({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const agents = useStore((s) => s.agents);
  const list = Object.values(agents)
    .filter((a) => a.kind !== "chat")
    .sort((a, b) => a.name.localeCompare(b.name));
  const m = useMenu();
  const a = agents[value];
  return (
    <>
      <button type="button" className={"btn apick" + (a ? "" : " none")} ref={m.ref} onClick={m.toggle} aria-haspopup="listbox">
        {a ? <Avatar agent={a} size={20} /> : <Bot size={14} />}
        <span>{a ? a.name : t("wfc.pickAgent")}</span>
        <ChevronDown size={13} />
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} className="flow-menu" width={260}>
        {list.length === 0 && <p className="small muted" style={{ padding: 8, margin: 0 }}>{t("wf.noAgents")}</p>}
        {list.map((x) => (
          <button
            key={x.id}
            type="button"
            className={"mi" + (x.id === value ? " on" : "")}
            role="option"
            aria-selected={x.id === value}
            onClick={() => {
              m.close();
              onChange(x.id);
            }}
          >
            <Avatar agent={x} size={22} />
            <span className="g">
              <b>{x.name}</b>
              {x.role && <span>{x.role}</span>}
            </span>
            {x.id === value && <Check size={14} />}
          </button>
        ))}
      </Popover>
    </>
  );
}

/** 高级: the step's id, for {{steps.<id>}} and the run records. */
function StepId({ s, onChange }: { s: WfStep; onChange: (id: string) => void }) {
  const [edit, setEdit] = useState(false);
  if (!edit)
    return (
      <button type="button" className="fid" title={t("auto.stepId")} onClick={() => setEdit(true)}>
        #{s.id}
      </button>
    );
  return <input className="fi sm mono fid-in" autoFocus value={s.id} aria-label={t("auto.stepId")} onChange={(e) => onChange(e.target.value.replace(/[^A-Za-z0-9_-]/g, ""))} onBlur={() => setEdit(false)} onKeyDown={(e) => e.key === "Enter" && setEdit(false)} />;
}

// ---------------------------------------------------------------- runs

function dur(a: string, b?: string) {
  const s = Math.max(0, Math.round(((b ? new Date(b).getTime() : Date.now()) - new Date(a).getTime()) / 1000));
  return s < 60 ? `${s}s` : s < 3600 ? `${Math.floor(s / 60)}m ${s % 60}s` : `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

function RunCard({ r, steps, open, toggle, onChanged }: { r: WfRun; steps: number; open: boolean; toggle: () => void; onChanged: (x?: WfRun) => void }) {
  const agents = useStore((s) => s.agents);
  const [busy, setBusy] = useState(false);
  const [detail, setDetail] = useState<WfRun>(r);
  useEffect(() => setDetail(r), [r]);
  useEffect(() => {
    if (open) void API.workflowRun(r.id).then(setDetail, () => {});
  }, [open, r.status]);
  const rerun = async (from: string) => {
    setBusy(true);
    try {
      const x = await API.rerun(r.id, from);
      toast(t("auto.rerunStarted", { step: from }));
      onChanged(x);
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const cancel = async () => {
    try {
      await API.cancelRun(r.id);
      onChanged();
    } catch (e) {
      toast(errMsg(e));
    }
  };
  const live = detail.status === "running" || detail.status === "waiting";
  const top = detail.steps.filter((s) => !s.group);
  const segs = Math.max(top.length, steps);
  return (
    <div className={"wrun" + (open ? " open" : "")}>
      <button className="wrh" type="button" aria-expanded={open} onClick={toggle}>
        <ChevronRight size={13} className="chv" />
        {runPill(detail.status)}
        <span className="g">
          {hhmm(detail.started)} · {ago(detail.started)}
          {detail.fromStep ? ` · ${t("auto.fromStep", { step: detail.fromStep })}` : ""}
          {detail.trigger && detail.trigger !== "manual" ? ` · ${scheduleLabel(detail.trigger)}` : ""}
        </span>
        <span className="wprog" aria-hidden>
          {Array.from({ length: segs }, (_, k) => (
            <i key={k} className={"st-" + (top[k]?.status ?? "pending")} />
          ))}
        </span>
        <span className="small muted wdur">{dur(detail.started, detail.ended)}</span>
      </button>
      {open && (
        <div className="wrb">
          {detail.input && (
            <div className="winput small">
              <span className="muted">{t("auto.input")}</span> {detail.input}
            </div>
          )}
          {detail.steps.map((s) => {
            const a = agents[s.agent ?? ""];
            return (
              <div className={"wrs" + (s.group ? " sub" : "")} key={(s.group ?? "") + s.id}>
                <div className="wrs-h">
                  <span className={"fnum sm st-" + s.status}>{STATUS_ICON[s.status] ?? "·"}</span>
                  {s.agent ? (
                    <span className="sline small">
                      <Avatar agent={a} size={16} />
                      <b>{a?.name ?? s.agent}</b>
                    </span>
                  ) : (
                    <b className="small">{s.kind === "approval" ? t("wf.approval") : s.kind === "parallel" ? t("auto.kind.parallel") : s.id}</b>
                  )}
                  <span className="small muted">{t(`auto.st.${s.status}` as Key)}</span>
                  {s.reused && <span className="tag">{t("auto.reused")}</span>}
                  <span className="sp" />
                  {!s.group && !live && (
                    <button className="btn ghost sm" type="button" disabled={busy} onClick={() => rerun(s.id)} title={t("auto.rerunFrom")}>
                      <RotateCcw size={12} />
                      {t("auto.rerunFrom")}
                    </button>
                  )}
                </div>
                {s.error && <div className="small errt">{s.error}</div>}
                {s.output && (
                  <div className="wout">
                    <Markdown text={s.output.length > 1200 ? s.output.slice(0, 1200) + "…" : s.output} />
                  </div>
                )}
              </div>
            );
          })}
          {detail.error && <div className="small errt">{detail.error}</div>}
          {live && (
            <div className="acts sm">
              <button className="btn dng sm" type="button" onClick={cancel}>
                <Square size={12} />
                {t("auto.cancelRun")}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
