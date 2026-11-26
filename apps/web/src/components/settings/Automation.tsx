import { EmptyState } from "../EmptyState";
import { Skeleton } from "../Skeleton";
import { lazy, Suspense, useEffect, useRef, useState } from "react";
import { CalendarClock, ChevronRight, Pencil, Play, Plus, Trash, X } from "lucide-react";
import { API, type Routine, type RoutineRun } from "../../api";
import { t, type Key } from "../../i18n";
import { ago, modeLabel, until } from "../../format";
import { openDesk, openNewAgent, openSession, toast, useStore } from "../../store";
import { Avatar } from "../Avatar";
import { Head } from "./Head";
import { TriggerPicker } from "./TriggerPicker";
import { confirmDialog } from "../Confirm";

// the conversation imports this file (做成例行): the workflows page loads when opened
const Workflows = lazy(() => import("./Workflows").then((m) => ({ default: m.Workflows })));

type Tab = "routines" | "workflows";
const TAB_KEY = "crewbase.autoTab";
function readTab(): Tab {
  try {
    return sessionStorage.getItem(TAB_KEY) === "workflows" ? "workflows" : "routines";
  } catch {
    return "routines";
  }
}

/** 设置 › 自动化: 例行巡检 + 晨报 and 工作流. */
export function Automation() {
  const [focus] = useState(() => {
    const f = focusNext;
    focusNext = null;
    return f;
  });
  // the tab you were on (an unsaved workflow brings you back to it)
  const [tab, setTabState] = useState<Tab>(() => focus?.tab ?? readTab());
  const setTab = (v: Tab) => {
    setTabState(v);
    try {
      sessionStorage.setItem(TAB_KEY, v);
    } catch {
      /* private mode */
    }
  };
  return (
    <>
      <Head
        title={t("set.auto")}
        sub={t("sub.auto")}
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
      {tab === "routines" ? (
        <Routines focus={focus?.tab === "routines" ? focus.id : undefined} />
      ) : (
        <Suspense fallback={<Skeleton kind="rows" />}>
          <Workflows focus={focus?.tab === "workflows" ? focus.id : undefined} />
        </Suspense>
      )}
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
export function runPill(status: string) {
  const k = `auto.st.${status}` as Key;
  return <span className={"pill " + (RUN_TONE[status] ?? "")}>{t(k) === k ? status : t(k)}</span>;
}

/** When a routine (or a drafted one) runs: the same picker as 工作流's
 *  何时运行; 手动 only where the presets offer it. */
export function ScheduleField({ value, onChange, presets = PRESETS, hint }: { value: string; onChange: (v: string) => void; presets?: string[]; hint?: string }) {
  return (
    <div>
      <TriggerPicker value={value} onChange={onChange} noManual={!presets.includes("manual")} />
      {hint && <span className="small">{hint}</span>}
    </div>
  );
}

/** A schedule the daemon refused, said in words (its parser speaks English). */
export function scheduleError(msg: string): string | null {
  if (/interval .* below the minimum/i.test(msg)) return t("auto.errInterval");
  if (/cron needs|want: |unknown weekday|bad time|empty schedule|minute:|hour:|day of month|month:|day of week/i.test(msg)) return t("auto.errSchedule");
  return null;
}

/** Opens 自动化 on a tab, with one routine or workflow brought into view
 *  (the link after creating one from a conversation). */
let focusNext: { tab: Tab; id: string } | null = null;
export function openAutomation(tab: Tab, id: string) {
  focusNext = { tab, id };
  openDesk("auto");
}

export function AgentSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const agents = useStore((s) => s.agents);
  const list = Object.values(agents)
    .filter((a) => a.kind !== "chat")
    .sort((a, b) => a.name.localeCompare(b.name));
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

export function ModeSeg({ value, onChange }: { value: string; onChange: (v: string) => void }) {
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

function Routines({ focus }: { focus?: string }) {
  const agents = useStore((s) => s.agents);
  const [list, setList] = useState<Routine[] | null>(null);
  const [edit, setEdit] = useState<Partial<Routine> | null>(null);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const noAgents = Object.values(agents).filter((a) => a.kind !== "chat").length === 0;
  const load = () => API.routines().then((r) => setList(r.routines ?? []), (e) => setErr(errMsg(e)));
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    if (focus && list?.some((r) => r.id === focus)) document.querySelector(`[data-routine="${CSS.escape(focus)}"]`)?.scrollIntoView({ block: "center" });
  }, [focus, list]);

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
    if (!(await confirmDialog(t("auto.deleteConfirm", { name: r.name })))) return;
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
      <div className="vsec row">
        <span>{t("auto.routines")}</span>
        <span className="sp" />
        {!edit && (
          <button className="btn" type="button" onClick={() => (noAgents ? openNewAgent() : setEdit({ mode: "read-only", schedule: "daily 08:30", enabled: true }))}>
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
      {list && list.length === 0 && !edit && (
        <EmptyState
          icon={<CalendarClock size={22} />}
          title={t("auto.noRoutines")}
          desc={noAgents ? t("auto.needAgent") : t("es.routinesDesc")}
          action={
            noAgents ? (
              <button className="btn pri" type="button" onClick={() => openNewAgent()}>
                <Plus size={13} />
                {t("auto.newAgentFirst")}
              </button>
            ) : undefined
          }
        />
      )}
      <div className="cards">
        {list?.map((r) => {
          const a = agents[r.agent];
          return (
            <div className={"rcard" + (r.enabled ? "" : " off") + (focus === r.id ? " focus" : "")} key={r.id} data-routine={r.id}>
              <div className="rch">
                <Avatar agent={a} size={30} />
                <div className="g">
                  <b title={r.name}>{r.name}</b>
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

export function RoutineForm({ init, onCancel, onSaved }: { init: Partial<Routine>; onCancel: () => void; onSaved: (r: Routine, created: boolean) => void }) {
  const [f, setF] = useState<Partial<Routine>>(init);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const set = (p: Partial<Routine>) => {
    setErr(null);
    setF((x) => ({ ...x, ...p }));
  };
  const formRef = useRef<HTMLDivElement>(null);
  const isNew = !init.id;
  const save = async () => {
    const missing = !f.name?.trim() ? "name" : !f.agent ? "agent" : !f.prompt?.trim() ? "prompt" : !f.schedule?.trim() ? "schedule" : "";
    if (missing) {
      setErr(t(`auto.need.${missing}` as Key));
      // the field itself, or the first control inside its wrapper
      const at = formRef.current?.querySelector<HTMLElement>(`[data-f="${missing}"]`);
      (at?.matches("input, select, textarea") ? at : at?.querySelector<HTMLElement>("select, input, textarea, button"))?.focus();
      return;
    }
    setBusy(true);
    try {
      const r = isNew
        ? await API.createRoutine({ name: (f.name ?? "").trim(), agent: f.agent!, prompt: f.prompt!, schedule: (f.schedule ?? "").trim(), mode: f.mode })
        : await API.patchRoutine(init.id!, { name: (f.name ?? "").trim(), prompt: f.prompt, schedule: (f.schedule ?? "").trim(), mode: f.mode });
      onSaved(r, isNew);
    } catch (e) {
      const m = errMsg(e);
      setErr(scheduleError(m) ?? m);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="box form" ref={formRef}>
      <b>{isNew ? t("auto.newRoutine") : t("auto.editRoutine", { name: init.name ?? "" })}</b>
      <div className="fgrid">
        <label className="fl">{t("auto.name")}</label>
        <input className="fi sm" data-f="name" autoFocus value={f.name ?? ""} placeholder={t("auto.namePh")} onChange={(e) => set({ name: e.target.value })} />
        <label className="fl">{t("auto.agent")}</label>
        <div data-f="agent">{isNew && !init.agent ? <AgentSelect value={f.agent ?? ""} onChange={(v) => set({ agent: v })} /> : <AgentName id={f.agent ?? ""} />}</div>
        <label className="fl">{t("auto.prompt")}</label>
        <textarea className="fi sm" data-f="prompt" rows={3} value={f.prompt ?? ""} placeholder={t("auto.promptPh")} onChange={(e) => set({ prompt: e.target.value })} />
        <label className="fl">{t("auto.schedule")}</label>
        <div data-f="schedule">
          <ScheduleField value={f.schedule ?? ""} onChange={(v) => set({ schedule: v })} />
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

export function RoutineRuns({ r }: { r: Routine }) {
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
  if (!runs) return <Skeleton />;
  return (
    <div className="runs">
      {runs.length === 0 && <p className="small">{t("auto.noRuns")}</p>}
      {runs.slice(0, 10).map((x) => (
        <div className="runrow" key={x.key + x.started}>
          {runPill(x.status)}
          <span className="small tm">{ago(x.started)}</span>
          <span className="g small">{x.status === "quiet" ? t("auto.quietNote") : x.summary || ""}</span>
          {r.session && (
            <button className="btn sm ghost" type="button" onClick={() => openSession(r.session)}>
              {t("appr.open")}
            </button>
          )}
        </div>
      ))}
    </div>
  );
}

