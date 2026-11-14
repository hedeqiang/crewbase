import { useEffect, useState } from "react";
import { ArrowDown, ArrowUp, ChevronRight, Hand, Pencil, Play, Plus, RotateCcw, Square, Sunrise, Trash, Users, X } from "lucide-react";
import { API, type AgentView, type Brief, type Routine, type RoutineRun, type WfRun, type WfStep, type Workflow } from "../../api";
import { t, type Key } from "../../i18n";
import { ago, hhmm, modeLabel, until } from "../../format";
import { openSession, toast, useStore } from "../../store";
import { Avatar } from "../Avatar";
import { Markdown } from "../Markdown";
import { Head } from "../Settings";

type Tab = "routines" | "workflows";

/** 设置 › 自动化: 例行巡检 + 晨报 and 工作流. */
export function Automation() {
  const [tab, setTab] = useState<Tab>("routines");
  return (
    <>
      <Head
        title={t("set.auto")}
        right={
          <div className="seg">
            <button type="button" aria-pressed={tab === "routines"} onClick={() => setTab("routines")}>
              {t("auto.routines")}
            </button>
            <button type="button" aria-pressed={tab === "workflows"} onClick={() => setTab("workflows")}>
              {t("auto.workflows")}
            </button>
          </div>
        }
      />
      {tab === "routines" ? <Routines /> : <Workflows />}
    </>
  );
}

function errMsg(e: any) {
  return e?.message ?? String(e);
}

const PRESETS = ["daily 08:30", "every 1h", "weekly mon 09:00", "webhook"];

export function scheduleLabel(s: string): string {
  if (!s || s === "manual") return t("auto.manual");
  if (s === "webhook") return t("auto.webhook");
  let m = /^daily (\d\d?:\d\d)$/i.exec(s);
  if (m) return t("auto.daily", { time: m[1] });
  m = /^every (\S+)$/i.exec(s);
  if (m) return t("auto.every", { d: m[1].replace(/h$/, t("auto.hour")).replace(/m$/, t("auto.minute")) });
  m = /^weekly (\w+) (\d\d?:\d\d)$/i.exec(s);
  if (m) return t("auto.weekly", { day: t(`auto.wd.${m[1].slice(0, 3).toLowerCase()}` as Key), time: m[2] });
  return s;
}

const RUN_TONE: Record<string, string> = { running: "blue", quiet: "", attention: "amber", failed: "red", completed: "ok", waiting: "amber", rejected: "red", cancelled: "" };
function runPill(status: string) {
  const k = `auto.st.${status}` as Key;
  return <span className={"pill " + (RUN_TONE[status] ?? "")}>{t(k) === k ? status : t(k)}</span>;
}

function AgentSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const agents = useStore((s) => s.agents);
  const list = Object.values(agents).sort((a, b) => a.name.localeCompare(b.name));
  return (
    <span className="ssel">
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{t("auto.pickAgent")}</option>
        {list.map((a) => (
          <option key={a.id} value={a.id}>
            {a.name}
            {a.role ? ` · ${a.role}` : ""}
          </option>
        ))}
      </select>
    </span>
  );
}

function ModeSeg({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="seg">
      {(["read-only", "sandbox"] as const).map((m) => (
        <button key={m} type="button" aria-pressed={value === m} onClick={() => onChange(m)}>
          {modeLabel(m)}
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------- routines

function Routines() {
  const agents = useStore((s) => s.agents);
  const [list, setList] = useState<Routine[] | null>(null);
  const [edit, setEdit] = useState<Partial<Routine> | null>(null);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const load = () => API.routines().then((r) => setList(r.routines ?? []), (e) => setErr(errMsg(e)));
  useEffect(() => {
    void load();
  }, []);

  const toggle = async (r: Routine) => {
    try {
      await API.patchRoutine(r.id, { enabled: !r.enabled });
      void load();
    } catch (e) {
      toast(errMsg(e));
    }
  };
  const runNow = async (r: Routine) => {
    try {
      await API.runRoutine(r.id);
      toast(t("auto.started", { name: r.name }));
      setOpen(r.id);
      window.setTimeout(load, 800);
    } catch (e) {
      toast(errMsg(e));
    }
  };
  const del = async (r: Routine) => {
    if (!window.confirm(t("auto.deleteConfirm", { name: r.name }))) return;
    try {
      await API.deleteRoutine(r.id);
      void load();
    } catch (e) {
      toast(errMsg(e));
    }
  };
  const saved = (r: Routine, created: boolean) => {
    setEdit(null);
    if (created && r.secret) setSecret({ name: r.name, secret: r.secret });
    toast(created ? t("auto.created", { name: r.name }) : t("panel.saved"));
    void load();
  };

  return (
    <div className="vb">
      <BriefPreview />
      <div className="vsec row">
        <span>{t("auto.routines")}</span>
        <span className="sp" />
        {!edit && (
          <button className="btn" type="button" disabled={Object.keys(agents).length === 0} onClick={() => setEdit({ mode: "read-only", schedule: "daily 08:30", enabled: true })}>
            <Plus size={13} />
            {t("auto.newRoutine")}
          </button>
        )}
      </div>
      <p className="small" style={{ margin: "0 0 10px" }}>
        {t("auto.routinesDesc")}
      </p>
      {secret && (
        <div className="box rulenote warnbox">
          <div style={{ minWidth: 0, flex: 1 }}>
            <b>{t("auto.secretTitle", { name: secret.name })}</b>
            <code className="cmd">{secret.secret}</code>
            <span className="small">{t("auto.secretNote")}</span>
          </div>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={() => setSecret(null)}>
            <X size={13} />
          </button>
        </div>
      )}
      {edit && <RoutineForm init={edit} onCancel={() => setEdit(null)} onSaved={saved} />}
      {err && <p className="small errt">{err}</p>}
      {list && list.length === 0 && !edit && <div className="empty">{t("auto.noRoutines")}</div>}
      <div className="cards">
        {list?.map((r) => {
          const a = agents[r.agent];
          return (
            <div className={"rcard" + (r.enabled ? "" : " off")} key={r.id}>
              <div className="rch">
                <Avatar agent={a} size={30} />
                <div className="g">
                  <b>{r.name}</b>
                  <span className="small">
                    {a?.name ?? r.agent} · {scheduleLabel(r.schedule)} · {modeLabel(r.mode)}
                  </span>
                </div>
                <button className="tg" type="button" role="switch" aria-checked={r.enabled} aria-label={t("auto.enabled")} onClick={() => toggle(r)} />
              </div>
              <p className="rprompt">{r.prompt}</p>
              <div className="rmeta small">
                {r.enabled && r.nextRun && <span>{t("auto.nextRun", { time: until(r.nextRun) })}</span>}
                {r.lastRun && <span>{t("auto.lastRun", { time: ago(r.lastRun) })}</span>}
              </div>
              <div className="acts sm">
                <button className="btn" type="button" onClick={() => runNow(r)}>
                  <Play size={12} />
                  {t("auto.runNow")}
                </button>
                <button className="btn ghost" type="button" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? null : r.id)}>
                  <ChevronRight size={12} className="chv" />
                  {t("auto.runs")}
                </button>
                <span className="sp" />
                <button className="ix" type="button" aria-label={t("auto.edit")} title={t("auto.edit")} onClick={() => setEdit(r)}>
                  <Pencil size={13} />
                </button>
                <button className="ix" type="button" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => del(r)}>
                  <Trash size={13} />
                </button>
              </div>
              {open === r.id && <RoutineRuns r={r} />}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function RoutineForm({ init, onCancel, onSaved }: { init: Partial<Routine>; onCancel: () => void; onSaved: (r: Routine, created: boolean) => void }) {
  const [f, setF] = useState<Partial<Routine>>(init);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const set = (p: Partial<Routine>) => setF((x) => ({ ...x, ...p }));
  const isNew = !init.id;
  const save = async () => {
    if (!f.name?.trim() || !f.agent || !f.prompt?.trim() || !f.schedule?.trim()) {
      setErr(t("auto.required"));
      return;
    }
    setBusy(true);
    try {
      const r = isNew
        ? await API.createRoutine({ name: f.name.trim(), agent: f.agent, prompt: f.prompt, schedule: f.schedule.trim(), mode: f.mode })
        : await API.patchRoutine(init.id!, { name: f.name.trim(), prompt: f.prompt, schedule: f.schedule.trim(), mode: f.mode });
      onSaved(r, isNew);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="box form">
      <b>{isNew ? t("auto.newRoutine") : t("auto.editRoutine", { name: init.name ?? "" })}</b>
      <div className="fgrid">
        <label className="fl">{t("auto.name")}</label>
        <input className="fi sm" value={f.name ?? ""} placeholder={t("auto.namePh")} onChange={(e) => set({ name: e.target.value })} />
        <label className="fl">{t("auto.agent")}</label>
        {isNew ? <AgentSelect value={f.agent ?? ""} onChange={(v) => set({ agent: v })} /> : <AgentName id={f.agent ?? ""} />}
        <label className="fl">{t("auto.prompt")}</label>
        <textarea className="fi sm" rows={3} value={f.prompt ?? ""} placeholder={t("auto.promptPh")} onChange={(e) => set({ prompt: e.target.value })} />
        <label className="fl">{t("auto.schedule")}</label>
        <div>
          <input className="fi sm mono" value={f.schedule ?? ""} onChange={(e) => set({ schedule: e.target.value })} />
          <div className="chips">
            {PRESETS.map((p) => (
              <button key={p} type="button" className="chip" aria-pressed={f.schedule === p} onClick={() => set({ schedule: p })}>
                {scheduleLabel(p)}
              </button>
            ))}
          </div>
          <span className="small">{t("auto.scheduleHint")}</span>
        </div>
        <label className="fl">{t("na.mode")}</label>
        <div>
          <ModeSeg value={f.mode || "read-only"} onChange={(v) => set({ mode: v })} />
          <div className="small" style={{ marginTop: 4 }}>
            {t("auto.modeHint")}
          </div>
        </div>
      </div>
      {err && <div className="ferr">{err}</div>}
      <div className="acts">
        <button className="btn pri" type="button" disabled={busy} onClick={save}>
          {isNew ? t("auto.create") : t("common.save")}
        </button>
        <button className="btn ghost" type="button" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}

function AgentName({ id }: { id: string }) {
  const a = useStore((s) => s.agents[id]);
  return (
    <span className="uchip static">
      <Avatar agent={a} size={18} />
      {a?.name ?? id}
    </span>
  );
}

function RoutineRuns({ r }: { r: Routine }) {
  const [runs, setRuns] = useState<RoutineRun[] | null>(null);
  useEffect(() => {
    let stop = false;
    const load = () => API.routineRuns(r.id).then((x) => !stop && setRuns(x.runs ?? []), () => !stop && setRuns([]));
    void load();
    const iv = window.setInterval(load, 4000);
    return () => {
      stop = true;
      window.clearInterval(iv);
    };
  }, [r.id]);
  if (!runs) return <p className="small">{t("common.loading")}</p>;
  return (
    <div className="runs">
      {runs.length === 0 && <p className="small">{t("auto.noRuns")}</p>}
      {runs.slice(0, 10).map((x) => (
        <div className="runrow" key={x.key + x.started}>
          {runPill(x.status)}
          <span className="small tm">{ago(x.started)}</span>
          <span className="g small">{x.status === "quiet" ? t("auto.quietNote") : x.summary || ""}</span>
          {r.session && (
            <button className="linkbtn" type="button" onClick={() => openSession(r.session)}>
              {t("appr.open")}
            </button>
          )}
        </div>
      ))}
    </div>
  );
}

function BriefPreview() {
  const agents = useStore((s) => s.agents);
  const [b, setB] = useState<{ preview: Brief; history: Brief[] } | null>(null);
  useEffect(() => {
    API.brief().then(setB, () => {});
  }, []);
  if (!b) return null;
  const p = b.preview;
  const needs = p.needsYou ?? [];
  const att = p.attention ?? [];
  const failed = p.failedAgents ?? [];
  const empty = needs.length + att.length + failed.length === 0;
  return (
    <div className="box brief2">
      <div className="bh">
        <Sunrise size={16} />
        <b>{t("auto.brief")}</b>
        <span className="small">{t("auto.briefSub")}</span>
      </div>
      {empty ? (
        <p className="small" style={{ margin: "6px 0 0" }}>
          {t("auto.briefEmpty", { n: p.folded })}
        </p>
      ) : (
        <ul className="blist">
          {needs.length > 0 && (
            <li>
              <span className="pill amber">{t("auto.briefNeeds", { n: needs.length })}</span>
              <span className="bt">
                {needs
                  .slice(0, 3)
                  .map((r) => `${agents[r.agent]?.name ?? (r.agent || t("appr.workflow"))}：${r.summary || r.tool}`)
                  .join("；")}
              </span>
            </li>
          )}
          {att.map((r) => (
            <li key={r.key}>
              <span className="pill amber">{t("auto.st.attention")}</span>
              <span className="bt">{r.summary || r.routine}</span>
            </li>
          ))}
          {failed.map((f) => (
            <li key={f.agent}>
              <span className="pill red">{t("auto.st.failed")}</span>
              <span className="bt">
                {f.name || f.agent}：{f.detail}
              </span>
            </li>
          ))}
          {p.folded > 0 && <li className="small">{t("auto.briefFolded", { n: p.folded })}</li>}
        </ul>
      )}
      {b.history.length > 0 && <p className="small" style={{ margin: "8px 0 0" }}>{t("auto.briefLast", { time: ago(b.history[b.history.length - 1].time) })}</p>}
    </div>
  );
}

// ---------------------------------------------------------------- workflows

function Workflows() {
  const [list, setList] = useState<Workflow[] | null>(null);
  const [sel, setSel] = useState<string | null>(null);
  const [draft, setDraft] = useState<Workflow | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const load = () =>
    API.workflows().then(
      (r) => {
        setList(r.workflows ?? []);
        return r.workflows ?? [];
      },
      (e) => {
        setErr(errMsg(e));
        return [];
      },
    );
  useEffect(() => {
    void load().then((l) => l[0] && setSel((s) => s ?? l[0].id));
  }, []);
  const cur = list?.find((w) => w.id === sel) ?? null;

  return (
    <div className="pvwrap">
      <div className="pvlist">
        <button
          className="btn addwf"
          type="button"
          onClick={() => {
            setSel(null);
            setDraft({ id: "", name: "", trigger: "manual", steps: [{ id: "step1", kind: "agent", agent: "", prompt: "{{input}}" }] });
          }}
        >
          <Plus size={13} />
          {t("auto.newWorkflow")}
        </button>
        {err && <p className="small errt">{err}</p>}
        {list?.length === 0 && !draft && <p className="small" style={{ padding: "0 8px" }}>{t("auto.noWorkflows")}</p>}
        {list?.map((w) => (
          <button
            key={w.id}
            type="button"
            className="pvi"
            aria-current={sel === w.id && !draft}
            onClick={() => {
              setDraft(null);
              setSel(w.id);
            }}
          >
            <span className="wfi">
              <Users size={14} />
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
      <div className="pvd">
        {draft ? (
          <WorkflowEditor
            key="new"
            init={draft}
            onCancel={() => setDraft(null)}
            onSaved={(w) => {
              setDraft(null);
              void load().then(() => setSel(w.id));
            }}
          />
        ) : cur ? (
          <WorkflowDetail key={cur.id} wf={cur} reload={() => void load()} onDeleted={() => void load().then((l) => setSel(l[0]?.id ?? null))} />
        ) : (
          <div className="empty">{t("auto.pickWorkflow")}</div>
        )}
      </div>
    </div>
  );
}

function WorkflowDetail({ wf, reload, onDeleted }: { wf: Workflow; reload: () => void; onDeleted: () => void }) {
  const [editing, setEditing] = useState(false);
  const [input, setInput] = useState("");
  const [runs, setRuns] = useState<WfRun[] | null>(null);
  const [openRun, setOpenRun] = useState<string | null>(null);
  const loadRuns = () => API.workflowRuns(wf.id).then((r) => setRuns(r.runs ?? []), () => setRuns([]));
  const active = runs?.some((r) => r.status === "running" || r.status === "waiting");
  useEffect(() => {
    void loadRuns();
  }, [wf.id]);
  useEffect(() => {
    if (!active) return;
    const iv = window.setInterval(loadRuns, 2500);
    return () => window.clearInterval(iv);
  }, [active, wf.id]);

  const run = async () => {
    try {
      const r = await API.runWorkflow(wf.id, input);
      setOpenRun(r.id);
      toast(t("auto.started", { name: wf.name }));
      void loadRuns();
    } catch (e) {
      toast(errMsg(e));
    }
  };
  const del = async () => {
    if (!window.confirm(t("auto.deleteConfirm", { name: wf.name }))) return;
    try {
      await API.deleteWorkflow(wf.id);
      onDeleted();
    } catch (e) {
      toast(errMsg(e));
    }
  };
  if (editing)
    return (
      <WorkflowEditor
        init={wf}
        onCancel={() => setEditing(false)}
        onSaved={() => {
          setEditing(false);
          reload();
        }}
      />
    );
  return (
    <>
      <div className="pvh">
        <div className="g">
          <b>{wf.name}</b>
          <span>
            {wf.id} · {scheduleLabel(wf.trigger)}
            {wf.nextRun ? ` · ${t("auto.nextRun", { time: until(wf.nextRun) })}` : ""}
          </span>
        </div>
        <button className="btn" type="button" onClick={() => setEditing(true)}>
          <Pencil size={13} />
          {t("auto.edit")}
        </button>
        <button className="ix" type="button" aria-label={t("common.delete")} title={t("common.delete")} onClick={del}>
          <Trash size={13} />
        </button>
      </div>
      {wf.description && <p className="small">{wf.description}</p>}
      {wf.hook && (
        <p className="small">
          {t("auto.hook")} <code>{wf.hook}</code>
        </p>
      )}
      <div className="psec2">{t("auto.pipeline")}</div>
      <Pipeline steps={wf.steps} />
      <div className="psec2">{t("auto.runIt")}</div>
      <div className="runbar">
        <input className="fi sm" value={input} placeholder={t("auto.inputPh")} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => e.key === "Enter" && run()} />
        <button className="btn pri" type="button" onClick={run}>
          <Play size={12} />
          {t("auto.runNow")}
        </button>
      </div>
      <div className="psec2">{t("auto.runs")}</div>
      {!runs ? (
        <p className="small">{t("common.loading")}</p>
      ) : runs.length === 0 ? (
        <p className="small">{t("auto.noRuns")}</p>
      ) : (
        <div className="wruns">
          {runs.slice(0, 15).map((r) => (
            <RunCard key={r.id} r={r} open={openRun === r.id} toggle={() => setOpenRun(openRun === r.id ? null : r.id)} onRerun={(x) => { setOpenRun(x.id); void loadRuns(); }} />
          ))}
        </div>
      )}
    </>
  );
}

function stepKindIcon(kind: string) {
  return kind === "approval" ? <Hand size={13} /> : kind === "parallel" ? <Users size={13} /> : null;
}

function Pipeline({ steps }: { steps: WfStep[] }) {
  const agents = useStore((s) => s.agents);
  return (
    <div className="pipe">
      {steps.map((s, i) => (
        <div className="pstep" key={s.id}>
          <span className="n">{i + 1}</span>
          <div className="g">
            <b>
              {stepKindIcon(s.kind)}
              {s.name || s.id}
              <span className="pill">{t(`auto.kind.${s.kind}` as Key)}</span>
            </b>
            {s.kind === "agent" && <StepAgentLine agent={agents[s.agent ?? ""]} id={s.agent} prompt={s.prompt} mode={s.mode} />}
            {s.kind === "parallel" &&
              (s.parallel ?? []).map((p) => <StepAgentLine key={p.id} agent={agents[p.agent ?? ""]} id={p.agent} prompt={p.prompt} mode={p.mode} />)}
            {s.kind === "approval" && <span className="small">{s.approval?.summary || t("auto.approvalDefault")}</span>}
          </div>
        </div>
      ))}
    </div>
  );
}

function StepAgentLine({ agent, id, prompt, mode }: { agent?: AgentView; id?: string; prompt?: string; mode?: string }) {
  return (
    <span className="sline small">
      <Avatar agent={agent} size={16} />
      <b>{agent?.name ?? id}</b>
      <span className="pv">{prompt}</span>
      {mode && <span className="tag">{modeLabel(mode)}</span>}
    </span>
  );
}

function RunCard({ r, open, toggle, onRerun }: { r: WfRun; open: boolean; toggle: () => void; onRerun: (r: WfRun) => void }) {
  const agents = useStore((s) => s.agents);
  const [busy, setBusy] = useState(false);
  const [detail, setDetail] = useState<WfRun>(r);
  useEffect(() => setDetail(r), [r]);
  const rerun = async (from: string) => {
    setBusy(true);
    try {
      const x = await API.rerun(r.id, from);
      toast(t("auto.rerunStarted", { step: from }));
      onRerun(x);
    } catch (e) {
      toast(errMsg(e));
    } finally {
      setBusy(false);
    }
  };
  const cancel = async () => {
    try {
      await API.cancelRun(r.id);
      setDetail(await API.workflowRun(r.id));
    } catch (e) {
      toast(errMsg(e));
    }
  };
  const live = detail.status === "running" || detail.status === "waiting";
  const top = detail.steps.filter((s) => !s.group);
  return (
    <div className="wrun">
      <button className="wrh" type="button" aria-expanded={open} onClick={toggle}>
        <ChevronRight size={13} className="chv" />
        {runPill(detail.status)}
        <span className="g">
          {hhmm(detail.started)} · {ago(detail.started)}
          {detail.fromStep ? ` · ${t("auto.fromStep", { step: detail.fromStep })}` : ""}
          {detail.trigger && detail.trigger !== "manual" ? ` · ${detail.trigger}` : ""}
        </span>
        <span className="dots2">
          {top.map((s) => (
            <i key={s.id} className={"sdot2 " + (RUN_TONE[s.status] ?? "")} title={`${s.id} · ${s.status}`} />
          ))}
        </span>
      </button>
      {open && (
        <div className="wrb">
          {detail.input && (
            <div className="small">
              {t("auto.input")}：{detail.input}
            </div>
          )}
          {detail.steps.map((s) => {
            const a = agents[s.agent ?? ""];
            return (
              <div className={"wstep" + (s.group ? " sub" : "")} key={(s.group ?? "") + s.id}>
                <div className="wsh">
                  {runPill(s.status)}
                  <b>{s.id}</b>
                  {s.agent && (
                    <span className="small sline">
                      <Avatar agent={a} size={14} />
                      {a?.name ?? s.agent}
                    </span>
                  )}
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
                  <div className="wout mt">
                    <Markdown text={s.output.length > 1200 ? s.output.slice(0, 1200) + "…" : s.output} />
                  </div>
                )}
              </div>
            );
          })}
          {detail.error && <div className="small errt">{detail.error}</div>}
          {live && (
            <div className="acts sm">
              <button className="btn dng" type="button" onClick={cancel}>
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

// ---- editor

function nextId(steps: WfStep[], base = "step"): string {
  const ids = new Set(steps.flatMap((s) => [s.id, ...(s.parallel ?? []).map((p) => p.id)]));
  for (let i = steps.length + 1; ; i++) if (!ids.has(base + i)) return base + i;
}

function WorkflowEditor({ init, onCancel, onSaved }: { init: Workflow; onCancel: () => void; onSaved: (w: Workflow) => void }) {
  const [w, setW] = useState<Workflow>(() => JSON.parse(JSON.stringify(init)));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const isNew = !init.id;
  const setStep = (i: number, p: Partial<WfStep>) => setW((x) => ({ ...x, steps: x.steps.map((s, j) => (j === i ? { ...s, ...p } : s)) }));
  const move = (i: number, j: number) =>
    setW((x) => {
      if (j < 0 || j >= x.steps.length) return x;
      const steps = [...x.steps];
      const [s] = steps.splice(i, 1);
      steps.splice(j, 0, s);
      return { ...x, steps };
    });
  const add = (kind: WfStep["kind"]) =>
    setW((x) => {
      const id = nextId(x.steps, kind === "approval" ? "approve" : "step");
      const st: WfStep =
        kind === "approval"
          ? { id, kind, approval: { summary: "" } }
          : kind === "parallel"
            ? { id, kind, parallel: [{ id: id + "a", kind: "agent", agent: "", prompt: "{{prev}}" }, { id: id + "b", kind: "agent", agent: "", prompt: "{{prev}}" }] }
            : { id, kind: "agent", agent: "", prompt: "{{prev}}" };
      return { ...x, steps: [...x.steps, st] };
    });
  const save = async () => {
    setBusy(true);
    setErr(null);
    const body: Workflow = {
      ...w,
      name: w.name.trim(),
      steps: w.steps.map((s) =>
        s.kind === "approval"
          ? { id: s.id, name: s.name, kind: s.kind, approval: s.approval }
          : s.kind === "parallel"
            ? { id: s.id, name: s.name, kind: s.kind, parallel: (s.parallel ?? []).map((p) => ({ ...p, kind: "agent" })) }
            : { id: s.id, name: s.name, kind: "agent", agent: s.agent, prompt: s.prompt, mode: s.mode || undefined },
      ),
    };
    try {
      const out = isNew ? await API.createWorkflow(body) : await API.putWorkflow(init.id, body);
      toast(isNew ? t("auto.created", { name: out.name }) : t("panel.saved"));
      onSaved(out);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="wfedit">
      <div className="fgrid">
        <label className="fl">{t("auto.name")}</label>
        <input className="fi sm" value={w.name} placeholder={t("auto.wfNamePh")} onChange={(e) => setW({ ...w, name: e.target.value })} />
        <label className="fl">{t("auto.desc")}</label>
        <input className="fi sm" value={w.description ?? ""} onChange={(e) => setW({ ...w, description: e.target.value })} />
        <label className="fl">{t("auto.trigger")}</label>
        <div>
          <input className="fi sm mono" value={w.trigger} onChange={(e) => setW({ ...w, trigger: e.target.value })} />
          <div className="chips">
            {["manual", ...PRESETS].map((p) => (
              <button key={p} type="button" className="chip" aria-pressed={w.trigger === p} onClick={() => setW({ ...w, trigger: p })}>
                {scheduleLabel(p)}
              </button>
            ))}
          </div>
        </div>
      </div>
      <div className="psec2">
        {t("auto.steps")} <span className="small" style={{ fontWeight: 400 }}>{t("auto.stepsHint")}</span>
      </div>
      <div className="esteps">
        {w.steps.map((s, i) => (
          <div className="estep" key={i}>
            <div className="esh">
              <span className="n">{i + 1}</span>
              <input className="fi sm mono sid" value={s.id} aria-label={t("auto.stepId")} onChange={(e) => setStep(i, { id: e.target.value })} />
              <span className="pill">{t(`auto.kind.${s.kind}` as Key)}</span>
              <span className="sp" />
              <button className="ix" type="button" aria-label={t("panel.up")} disabled={i === 0} onClick={() => move(i, i - 1)}>
                <ArrowUp size={13} />
              </button>
              <button className="ix" type="button" aria-label={t("panel.down")} disabled={i === w.steps.length - 1} onClick={() => move(i, i + 1)}>
                <ArrowDown size={13} />
              </button>
              <button className="ix" type="button" aria-label={t("panel.remove")} disabled={w.steps.length === 1} onClick={() => setW({ ...w, steps: w.steps.filter((_, j) => j !== i) })}>
                <X size={13} />
              </button>
            </div>
            {s.kind === "agent" && <AgentStepFields s={s} onChange={(p) => setStep(i, p)} />}
            {s.kind === "approval" && (
              <input
                className="fi sm"
                value={s.approval?.summary ?? ""}
                placeholder={t("auto.approvalPh")}
                onChange={(e) => setStep(i, { approval: { summary: e.target.value } })}
              />
            )}
            {s.kind === "parallel" && (
              <div className="psubs">
                {(s.parallel ?? []).map((p, j) => (
                  <div className="psub" key={j}>
                    <div className="esh">
                      <input
                        className="fi sm mono sid"
                        value={p.id}
                        aria-label={t("auto.stepId")}
                        onChange={(e) => setStep(i, { parallel: (s.parallel ?? []).map((x, k) => (k === j ? { ...x, id: e.target.value } : x)) })}
                      />
                      <span className="sp" />
                      <button
                        className="ix"
                        type="button"
                        aria-label={t("panel.remove")}
                        disabled={(s.parallel ?? []).length <= 1}
                        onClick={() => setStep(i, { parallel: (s.parallel ?? []).filter((_, k) => k !== j) })}
                      >
                        <X size={13} />
                      </button>
                    </div>
                    <AgentStepFields s={p} onChange={(q) => setStep(i, { parallel: (s.parallel ?? []).map((x, k) => (k === j ? { ...x, ...q } : x)) })} />
                  </div>
                ))}
                <button
                  className="btn ghost sm"
                  type="button"
                  onClick={() => setStep(i, { parallel: [...(s.parallel ?? []), { id: s.id + String.fromCharCode(97 + (s.parallel ?? []).length), kind: "agent", agent: "", prompt: "{{prev}}" }] })}
                >
                  <Plus size={12} />
                  {t("auto.addBranch")}
                </button>
              </div>
            )}
          </div>
        ))}
      </div>
      <div className="acts">
        <button className="btn" type="button" onClick={() => add("agent")}>
          <Plus size={13} />
          {t("auto.addAgentStep")}
        </button>
        <button className="btn" type="button" onClick={() => add("parallel")}>
          <Users size={13} />
          {t("auto.addParallel")}
        </button>
        <button className="btn" type="button" onClick={() => add("approval")}>
          <Hand size={13} />
          {t("auto.addApproval")}
        </button>
      </div>
      <p className="small">{t("auto.templateHint")}</p>
      {err && <div className="ferr">{err}</div>}
      <div className="acts">
        <button className="btn pri" type="button" disabled={busy} onClick={save}>
          {isNew ? t("auto.create") : t("common.save")}
        </button>
        <button className="btn ghost" type="button" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}

function AgentStepFields({ s, onChange }: { s: WfStep; onChange: (p: Partial<WfStep>) => void }) {
  return (
    <div className="asf">
      <AgentSelect value={s.agent ?? ""} onChange={(v) => onChange({ agent: v })} />
      <ModeSeg value={s.mode || "sandbox"} onChange={(v) => onChange({ mode: v })} />
      <textarea className="fi sm" rows={2} value={s.prompt ?? ""} placeholder={t("auto.stepPromptPh")} onChange={(e) => onChange({ prompt: e.target.value })} />
    </div>
  );
}
