// The right panel's 定时 tab: when this agent starts work on its own — the
// routines the person scheduled for it (设置 › 自动化, shown here for this
// agent only) and the follow-ups it set for itself (followup_set).

import { Skeleton } from "./Skeleton";
import { useCallback, useEffect, useState } from "react";
import { AlarmClock, ChevronRight, MessageSquare, Pencil, Play, Trash, X } from "lucide-react";
import { API, type AgentView, type Followup, type Routine } from "../api";
import { t } from "../i18n";
import { ago, modeLabel, until } from "../format";
import { openSession, openSettings, toast, useStore } from "../store";
import { confirmDialog } from "./Confirm";
import { RoutineForm, RoutineRuns, scheduleLabel } from "./settings/Automation";

export interface ScheduleData {
  routines: Routine[] | null;
  followups: Followup[] | null;
  reload: () => void;
}

/** This agent's routines and follow-ups, refreshed while the panel is open
 *  (and whenever the agent's status changes: follow-ups are set and come
 *  back in its turns). */
export function useSchedule(agent: AgentView | undefined): ScheduleData {
  const [routines, setRoutines] = useState<Routine[] | null>(null);
  const [followups, setFollowups] = useState<Followup[] | null>(null);
  const id = agent && agent.kind !== "chat" ? agent.id : "";
  const reload = useCallback(() => {
    if (!id) return;
    API.routines().then(
      (r) => setRoutines((r.routines ?? []).filter((x) => x.agent === id)),
      () => setRoutines([]),
    );
    API.followups(id).then(
      (r) => setFollowups(r.followups ?? []),
      () => setFollowups([]),
    );
  }, [id]);
  useEffect(() => {
    setRoutines(null);
    setFollowups(null);
    reload();
  }, [reload]);
  useEffect(() => {
    reload();
  }, [agent?.status]);
  useEffect(() => {
    const h = setInterval(() => document.visibilityState === "visible" && reload(), 30_000);
    return () => clearInterval(h);
  }, [reload]);
  return { routines, followups, reload };
}

/** How many things are scheduled (the tab's count). */
export function scheduleCount(d: ScheduleData): number {
  return (d.routines?.filter((r) => r.enabled).length ?? 0) + (d.followups?.length ?? 0);
}

export function ScheduleTab({ agent, data }: { agent: AgentView; data: ScheduleData }) {
  const { routines, followups, reload } = data;
  // a phone sees and runs routines; changing them is the computer's
  const manage = useStore((s) => s.tier === "desktop");
  const [edit, setEdit] = useState<Partial<Routine> | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [secret, setSecret] = useState<{ name: string; secret: string } | null>(null);

  const err = (e: any) => toast(e?.message ?? String(e));
  const toggle = async (r: Routine) => {
    try {
      await API.patchRoutine(r.id, { enabled: !r.enabled });
      reload();
    } catch (e) {
      err(e);
    }
  };
  const runNow = async (r: Routine) => {
    try {
      await API.runRoutine(r.id);
      toast(t("auto.started", { name: r.name }));
      setOpen(r.id);
      window.setTimeout(reload, 800);
    } catch (e) {
      err(e);
    }
  };
  const del = async (r: Routine) => {
    if (!(await confirmDialog(t("auto.deleteConfirm", { name: r.name })))) return;
    try {
      await API.deleteRoutine(r.id);
      reload();
    } catch (e) {
      err(e);
    }
  };
  const cancelFollowup = async (f: Followup) => {
    try {
      await API.cancelFollowup(agent.id, f.id);
      reload();
    } catch (e) {
      err(e);
    }
  };

  return (
    <div className="sched">
      <div className="psec2">
        <span>{t("auto.routines")}</span>
      </div>
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
      {edit && (
        <RoutineForm
          key={edit.id ?? "new"}
          init={edit}
          onCancel={() => setEdit(null)}
          onSaved={(r, created) => {
            setEdit(null);
            if (created && r.secret) setSecret({ name: r.name, secret: r.secret });
            toast(created ? t("auto.created", { name: r.name }) : t("panel.saved"));
            reload();
          }}
        />
      )}
      {routines === null ? (
        <Skeleton />
      ) : routines.length === 0 ? (
        !edit && <SetupHint name={agent.name} />
      ) : (
        <div className="schedlist">
          {routines.map((r) => (
            <div className={"rcard" + (r.enabled ? "" : " off")} key={r.id}>
              <div className="rch">
                <span className="schedic">
                  <AlarmClock size={14} />
                </span>
                <div className="g">
                  <b title={r.name}>{r.name}</b>
                  <span className="small">
                    {scheduleLabel(r.schedule)} · {modeLabel(r.mode)}
                  </span>
                </div>
                {manage ? (
                  <button className="tg" type="button" role="switch" aria-checked={r.enabled} aria-label={t("auto.enabled")} onClick={() => void toggle(r)} />
                ) : (
                  !r.enabled && <span className="pill sm">{t("aprx.off")}</span>
                )}
              </div>
              <p className="rprompt">{r.prompt}</p>
              <div className="rorigin small">
                {r.by === "agent" ? (
                  r.origin ? (
                    <button className="linkbtn" type="button" onClick={() => r.origin && openSession(r.origin)}>
                      {t("sched.byAgent", { name: agent.name })}
                    </button>
                  ) : (
                    <span>{t("sched.byAgent", { name: agent.name })}</span>
                  )
                ) : (
                  <span>{t("sched.byYou")}</span>
                )}
              </div>
              <div className="rmeta small">
                {r.enabled && r.nextRun && <span className="pill blue sm">{t("auto.nextRun", { time: until(r.nextRun) })}</span>}
                {r.lastRun && <span>{t("auto.lastRun", { time: ago(r.lastRun) })}</span>}
              </div>
              <div className="acts sm">
                <button className="btn" type="button" onClick={() => void runNow(r)}>
                  <Play size={12} />
                  {t("auto.runNow")}
                </button>
                <button className="btn ghost" type="button" aria-expanded={open === r.id} onClick={() => setOpen(open === r.id ? null : r.id)}>
                  <ChevronRight size={12} className="chv" />
                  {t("auto.runs")}
                </button>
                <span className="sp" />
                {manage && (
                  <>
                    <button className="ix" type="button" aria-label={t("auto.edit")} title={t("auto.edit")} onClick={() => setEdit(r)}>
                      <Pencil size={13} />
                    </button>
                    <button className="ix" type="button" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => void del(r)}>
                      <Trash size={13} />
                    </button>
                  </>
                )}
              </div>
              {open === r.id && <RoutineRuns r={r} />}
            </div>
          ))}
        </div>
      )}

      <div className="psec2">
        <span>{t("sched.followups")}</span>
      </div>
      {followups === null ? null : followups.length === 0 ? (
        <p className="small schedempty">{t("sched.noFollowups", { name: agent.name })}</p>
      ) : (
        followups.map((f) => (
          <div className="qi" key={f.id}>
            <span className="n">
              <AlarmClock size={12} />
            </span>
            <span className="g">
              <span className="qt">{f.note}</span>
              <span className="small" style={{ display: "block" }}>
                {dueLabel(f.due)}
              </span>
            </span>
            <button className="ix" type="button" aria-label={t("fu.cancel")} title={t("fu.cancel")} onClick={() => void cancelFollowup(f)}>
              <X size={13} />
            </button>
          </div>
        ))
      )}

      {routines !== null && routines.length > 0 && !edit && <SetupHint name={agent.name} more />}
      {manage ? (
        <button className="linkbtn schedall" type="button" onClick={() => openSettings("auto")}>
          {t("sched.all")}
        </button>
      ) : (
        <p className="small muted schedall">{t("aprx.schedOnDesktop")}</p>
      )}
    </div>
  );
}

export function dueLabel(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(d) - day(now)) / 86400000);
  if (diff === 0) return t("fu.today", { time: hm });
  if (diff === 1) return t("fu.tomorrow", { time: hm });
  return t("fu.on", { date: d.toLocaleDateString([], { month: "numeric", day: "numeric" }), time: hm });
}

/** Routines are set up by asking the agent; this says how. */
function SetupHint({ name, more = false }: { name: string; more?: boolean }) {
  return (
    <div className={"schedhint" + (more ? " more" : "")}>
      <MessageSquare size={14} />
      <span>
        {more ? t("sched.askMore", { name }) : t("sched.ask", { name })}
        <span className="ex">{t("sched.askExample")}</span>
      </span>
    </div>
  );
}
