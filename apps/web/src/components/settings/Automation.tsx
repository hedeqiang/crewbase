import { EmptyState } from "../EmptyState";
import { Skeleton } from "../Skeleton";
import { useEffect, useState } from "react";
import { CalendarClock, ChevronRight, Pencil, Play, Plus, Trash, X } from "lucide-react";
import { API, type Routine, type RoutineRun } from "../../api";
import { t, type Key } from "../../i18n";
import { ago, modeLabel, until } from "../../format";
import { openSession, toast, useStore } from "../../store";
import { Avatar } from "../Avatar";
import { Head } from "../Settings";
import { Workflows } from "./Workflows";
import { confirmDialog } from "../Confirm";
import { NightShiftSettings } from "../NightShift";

type Tab = "routines" | "nightshift" | "workflows";

/** 设置 › 自动化: 例行巡检 + 晨报 and 工作流. */
export function Automation() {
  const [tab, setTab] = useState<Tab>("routines");
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
            <button type="button" aria-pressed={tab === "nightshift"} onClick={() => setTab("nightshift")}>
              {t("ns.title")}
            </button>
            <button type="button" aria-pressed={tab === "workflows"} onClick={() => setTab("workflows")}>
              {t("auto.workflows")}
            </button>
          </div>
        }
      />
      {tab === "routines" ? <Routines /> : tab === "nightshift" ? <NightShiftSettings /> : <Workflows />}
    </>
  );
}

function errMsg(e: any) {
  return e?.message ?? String(e);
}

const PRESETS = ["daily 08:30", "every 1h", "weekly mon 09:00", "webhook"];

export function scheduleLabel(s: string): string {
  // a zone and a daily window (features/schedule window.go): "· 22:00–08:00"
  const w = /^(?:TZ=\S+\s+)?(.*?)(?:\s+during\s+(\d\d?:\d\d)-(\d\d?:\d\d))?$/i.exec(s.trim());
  if (w && (w[2] || w[1] !== s.trim())) return scheduleLabel(w[1]) + (w[2] ? ` · ${w[2]}–${w[3]}` : "");
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
      {list && list.length === 0 && !edit && <EmptyState icon={<CalendarClock size={22} />} title={t("auto.noRoutines")} desc={t("es.routinesDesc")} />}
      <div className="cards">
        {list?.map((r) => {
          const a = agents[r.agent];
          return (
            <div className={"rcard" + (r.enabled ? "" : " off")} key={r.id}>
              <div className="rch">
                <Avatar agent={a} size={30} />
                <div className="g">
                  <b title={r.name}>{r.name}</b>
                  <span className="small">
                    {a?.name ?? r.agent} · {scheduleLabel(r.schedule)} · {modeLabel(r.mode)}
                  </span>
                </div>
                {r.by === "nightshift" ? (
                  <span className="pill sm">{t("ns.managed")}</span>
                ) : (
                  <button className="tg" type="button" role="switch" aria-checked={r.enabled} aria-label={t("auto.enabled")} onClick={() => toggle(r)} />
                )}
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
                {r.by !== "nightshift" && (
                  <>
                    <button className="ix" type="button" aria-label={t("auto.edit")} title={t("auto.edit")} onClick={() => setEdit(r)}>
                      <Pencil size={13} />
                    </button>
                    <button className="ix" type="button" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => del(r)}>
                      <Trash size={13} />
                    </button>
                  </>
                )}
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
        {isNew && !init.agent ? <AgentSelect value={f.agent ?? ""} onChange={(v) => set({ agent: v })} /> : <AgentName id={f.agent ?? ""} />}
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

