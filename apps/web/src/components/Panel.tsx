import { TaskBoard } from "./TaskBoard";
import { WorkNotes, WorkTasks } from "./Working";
import { useEffect, useMemo, useState, useRef } from "react";
import { Archive, ArrowDown, ArrowUp, Check, ChevronLeft, ChevronRight, CircleMinus, CircleX, Globe, GripVertical, Link2, Minimize2, Pause, Play, Plus, Shuffle, Square, Trash, UserPlus, X } from "lucide-react";
import { API, refString, type AgentView, type ChannelView, type ContextBreakdown, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { activeModel, allModels, detailLabel, effortLabel, exhaustedHint, exhaustedLabel, hhmm, isExhausted, kfmt, modeLabel, modelLabel, modelMeta, modelOff, pillTone, sameRef, statusLabel, until } from "../format";
import { project, type SubRun } from "../projection";
import { openSubRun, refreshChannel, setState, toast, useStore, sessionAgent } from "../store";
import { FilesTab } from "./Files";
import { LibraryTab } from "./Library";
import { ScheduleTab, scheduleCount, useSchedule } from "./Schedule";
import { PreviewTab } from "./Preview";
import { Avatar, LookPicker } from "./Avatar";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";
import { shortPath, SubFeed } from "./Conversation";
import { PanelView, useSlot } from "./UISlots";
import { confirmDialog } from "./Confirm";
import { AgentConnectors } from "./AgentConnectors";

export function Panel() {
  const current = useStore((s) => s.current);
  const tab = useStore((s) => s.panelTab);
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const channels = useStore((s) => s.channels);
  const agent = useMemo(() => sessionAgent(current), [current, agents, sessions, channels]);
  const channel = current ? channels[current] : undefined;
  const pluginTabs = useSlot("sidebar-right.tab");
  const subView = useStore((s) => s.subView);
  const sched = useSchedule(agent);
  const nSched = scheduleCount(sched);
  const tabsRef = useRef<HTMLDivElement>(null);
  // the open tab stays in sight
  useEffect(() => {
    const el = tabsRef.current?.querySelector<HTMLElement>(`[data-tab="${CSS.escape(tab)}"]`);
    el?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [tab, agent?.id]);
  if (!agent) return null;
  const chat = agent.kind === "chat";
  const view: typeof tab = chat && (tab === "files" || tab === "library" || tab === "schedule") ? "task" : tab;
  // a plain chat has no workspace to browse (an HTML answer can still be previewed)
  const tabs: [typeof tab, string][] = [
    ["task", t("panel.task")],
    ...(chat ? [] : ([["schedule", t("sched.tab")]] as [typeof tab, string][])),
    ...(chat ? [] : ([["files", t("panel.files")]] as [typeof tab, string][])),
    ["preview", t("panel.preview")],
    ...(chat ? [] : ([["library", t("lib.title")]] as [typeof tab, string][])),
    ["agent", agent.kind === "chat" ? t("chat.settingsTab") : agent.name || t("panel.agent")],
    ...pluginTabs.map((c): [typeof tab, string] => [`ui:${c.key}`, c.title || c.key]),
  ];
  const pluginTab = view.startsWith("ui:") ? pluginTabs.find((c) => "ui:" + c.key === view) : undefined;
  return (
    <aside className={"panel" + (view === "preview" ? " wide" : view === "files" ? " files-wide" : "")}>
      <div className="ptabs">
        {/* the tabs scroll sideways when the panel is narrow; the close button stays */}
        <div className="ptabsc" role="tablist" ref={tabsRef}>
          {tabs.map(([k, l]) => (
            <button key={k} className="ptab" type="button" role="tab" aria-selected={view === k} data-tab={k} title={l} onClick={() => setState({ panelTab: k })}>
              <span className="ptabl">{l}</span>
              {k === "schedule" && nSched > 0 && <span className="ptabn">{nSched}</span>}
            </button>
          ))}
        </div>
        <button className="ix" type="button" aria-label={t("common.close")} onClick={() => setState({ panelOpen: false })}>
          <X size={13} />
        </button>
      </div>
      <div className="pbody">
        {subView && view === "task" ? (
          <SubRunView key={subView} session={subView} parentSession={current ?? agent.session} />
        ) : (
          <>
        {/* siblings: keys must differ (in a channel current is the channel id) */}
        {view === "task" && (channel ? <ChannelTask key={"ch:" + channel.id} channel={channel} /> : <TaskTab agent={agent} />)}
        {view === "task" && <SubRuns session={current ?? agent.session} />}
        {view === "task" && agent.kind !== "chat" && <TaskBoard key={"board:" + (current ?? agent.session)} session={current ?? agent.session} />}
          </>
        )}
        {view === "files" && <FilesTab agent={agent} />}
        {view === "library" && <LibraryTab />}
        {view === "schedule" && <ScheduleTab key={"sched:" + agent.id} agent={agent} data={sched} />}
        {view === "preview" && <PreviewTab key={"prv:" + (current ?? agent.session)} session={current ?? agent.session} />}
        {view === "agent" && <AgentTab key={"agent:" + agent.id} agent={agent} sessionId={current ?? agent.session} />}
        {pluginTab && (
          <div className="uitab">
            <div className="uitabh">
              <span className="small muted">{t("ui.from", { owner: pluginTab.owner })}</span>
            </div>
            <PanelView key={pluginTab.key + (current ?? "")} c={pluginTab} session={current ?? agent.session} />
          </div>
        )}
      </div>
    </aside>
  );
}

function ChannelTask({ channel }: { channel: ChannelView }) {
  const agents = useStore((s) => s.agents);
  const models = useStore((s) => s.models);
  const owner = agents[channel.owner];
  const re = useMenu();
  const addM = useMenu();
  const [busy, setBusy] = useState(false);
  const archived = channel.state === "archived";
  const run = async (fn: () => Promise<unknown>, ok?: string) => {
    setBusy(true);
    try {
      await fn();
      if (ok) toast(ok);
      await refreshChannel(channel.id);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const others = Object.values(agents).filter((a) => !channel.members.includes(a.id) && a.kind !== "chat");
  const m = owner ? activeModel(models, owner) : null;
  const chQueue = (a: AgentView) => a.queue.filter((q) => q.session === channel.id);
  // Stop only means something while a member is working or has queued work here.
  const working = channel.members.some((id) => {
    const a = agents[id];
    return !!a && (a.status === "running" || a.status === "waiting" || chQueue(a).length > 0);
  });

  return (
    <>
      <div className="psec">{t("ch.ownerSec")}</div>
      <div className={"ownerban" + (channel.state === "paused" ? " paused" : "") + (archived ? " arch" : "")}>
        <div className="oh">
          <Avatar agent={owner} size={34} status />
          <div className="g">
            <span className="small">{t("ch.owner")}</span>
            <b>{owner?.name ?? channel.owner}</b>
          </div>
          <span className={"pill " + (archived ? "" : channel.state === "paused" ? "" : owner ? pillTone(owner.status) : "")}>
            {archived ? t("ch.archived") : channel.state === "paused" ? t("ch.paused") : owner ? statusLabel(owner.status) : "—"}
          </span>
        </div>
        {owner?.detail && <div className="small od">{detailLabel(owner.detail)}</div>}
        {m && (
          <div className="small od">
            <ProviderMark provider={m.provider} model={m.model} size={12} /> {modelLabel(models, m)}
          </div>
        )}
        <div className="acts">
          {channel.state === "paused" ? (
            <button className="btn" type="button" disabled={busy || archived} onClick={() => run(() => API.channelAction(channel.id, "resume"), t("ch.resumedToast"))}>
              <Play size={12} />
              {t("panel.resume")}
            </button>
          ) : (
            <button className="btn" type="button" disabled={busy || archived || !working} onClick={() => run(() => API.channelAction(channel.id, "stop"), t("ch.stoppedToast"))}>
              <Square size={12} />
              {t("panel.stop")}
            </button>
          )}
          <button className="btn" type="button" ref={re.ref} disabled={busy || archived || channel.members.length < 2} onClick={re.toggle} aria-haspopup="menu">
            <Shuffle size={12} />
            {t("ch.reassign")}
          </button>
          <button
            className="btn ghost"
            type="button"
            disabled={busy || archived}
            onClick={async () => (await confirmDialog({ text: t("ch.archiveConfirm", { name: channel.title || channel.id }), danger: false })) && run(() => API.channelAction(channel.id, "archive"), t("ch.archivedToast"))}
          >
            <Archive size={12} />
            {t("ch.archive")}
          </button>
        </div>
      </div>

      <div className="psec">{t("ch.members", { n: channel.members.length })}</div>
      {channel.members.map((id) => {
        const a = agents[id];
        const q = a ? chQueue(a) : [];
        return (
          <div className="qi mem" key={id}>
            <Avatar agent={a} size={24} status />
            <span className="g">
              <b>{a?.name ?? id}</b>
              <span className="small" style={{ display: "block" }}>
                {id === channel.owner ? t("ch.owner") + " · " : ""}
                {a ? statusLabel(a.status) : "—"}
                {a ? ` · ${t("ch.queueLen", { n: a.queue.length })}` : ""}
              </span>
              {q.map((x) => (
                <span key={x.id} className="qt small" style={{ display: "block" }}>
                  {x.text}
                </span>
              ))}
            </span>
          </div>
        );
      })}
      {!archived && (
        <button className="btn ghost addm" type="button" ref={addM.ref} onClick={addM.toggle} disabled={others.length === 0} title={others.length === 0 ? t("ch.noOthers") : undefined} style={{ marginTop: 6 }}>
          <UserPlus size={13} />
          {t("ch.addMember")}
        </button>
      )}
      {!archived && others.length === 0 && (
        <p className="small muted" style={{ margin: "4px 0 0" }}>
          {t("ch.noOthers")}
        </p>
      )}

      <div className="psec">{t("ch.settings")}</div>
      <div className="tglist">
        <div className="tgrow">
          <span className="g">
            <b>{t("ch.handoverConfirm")}</b>
            <span>{t("ch.handoverConfirmDesc")}</span>
          </span>
          <button
            className="tg"
            type="button"
            role="switch"
            aria-checked={channel.settings?.handoverConfirm ?? false}
            aria-label={t("ch.handoverConfirm")}
            disabled={busy || archived}
            onClick={() => run(() => API.patchChannel(channel.id, { handoverConfirm: !channel.settings?.handoverConfirm }))}
          />
        </div>
      </div>

      <Popover anchor={re.anchor} open={re.open} onClose={re.close} width={280}>
        <div className="mlbl">{t("ch.reassignTo")}</div>
        {channel.members
          .filter((id) => id !== channel.owner)
          .map((id) => {
            const a = agents[id];
            return (
              <button
                key={id}
                className="mi"
                type="button"
                onClick={() => {
                  re.close();
                  void run(() => API.reassign(channel.id, id), t("ch.reassignedToast", { name: a?.name ?? id }));
                }}
              >
                <Avatar agent={a} size={22} status />
                <span className="g">
                  {a?.name ?? id}
                  <span>
                    {a ? statusLabel(a.status) : ""} · {t("ch.queueLen", { n: a?.queue.length ?? 0 })}
                  </span>
                </span>
              </button>
            );
          })}
        <hr />
        <p className="small" style={{ margin: "4px 9px 6px" }}>
          {t("ch.reassignNote")}
        </p>
      </Popover>
      <Popover anchor={addM.anchor} open={addM.open} onClose={addM.close} width={260}>
        {others.map((a) => (
          <button
            key={a.id}
            className="mi"
            type="button"
            onClick={() => {
              addM.close();
              void run(() => API.addMember(channel.id, a.id), t("ch.memberAdded", { name: a.name }));
            }}
          >
            <Avatar agent={a} size={22} />
            <span className="g">
              {a.name}
              <span>{a.role || a.id}</span>
            </span>
          </button>
        ))}
      </Popover>
    </>
  );
}

/** The sub-agents this conversation started: what each was asked, how it
 *  went; click one to see its whole run. */
function SubRuns({ session }: { session: string }) {
  const events = useStore((s) => s.events[session]);
  const runs = useMemo(() => project(events ?? []).subruns.slice().reverse(), [events]);
  if (runs.length === 0) return null;
  return (
    <>
      <div className="psec">{t("sub.title", { n: runs.length })}</div>
      <div className="subruns">
        {runs.map((r) => (
          <button key={r.session} className="subrun" type="button" onClick={() => openSubRun(r.session)}>
            <span className="srst">{runIcon(r)}</span>
            <span className="g">
              <span className="srtask">{r.task || t("sub.noTask")}</span>
              <span className="small muted">
                {runState(r)} · {hhmm(r.started)}
                {r.provider && r.provider !== "local" ? " · " + r.provider : ""}
              </span>
            </span>
            <ChevronRight size={14} className="muted" />
          </button>
        ))}
      </div>
    </>
  );
}

const runIcon = (r: SubRun) =>
  !r.finished ? <span className="spin sm" /> : r.stop === "completed" ? <Check size={13} className="okc" /> : r.stop === "cancelled" ? <CircleMinus size={13} className="muted" /> : <CircleX size={13} className="errc" />;
const runState = (r: SubRun) => t(!r.finished ? "sub.running" : r.stop === "completed" ? "sub.done" : r.stop === "cancelled" ? "sub.cancelled" : "sub.failed");

/** One sub-agent run, in the panel: what it was asked and everything it did. */
function SubRunView({ session, parentSession }: { session: string; parentSession: string }) {
  const parentEvents = useStore((s) => s.events[parentSession]);
  const run = useMemo(() => project(parentEvents ?? []).subruns.find((r) => r.session === session), [parentEvents, session]);
  useStore((s) => s.agents); // re-render when agents change
  const parent = sessionAgent(parentSession);
  return (
    <div className="subview">
      <div className="subhead">
        <button className="ix" type="button" aria-label={t("ap.back")} onClick={() => setState({ subView: null })}>
          <ChevronLeft size={15} />
        </button>
        <span className="g">
          <b>{t("sub.run")}</b>
          {run && (
            <span className="small muted">
              {runState(run)} · {hhmm(run.started)}
            </span>
          )}
        </span>
        {run && <span className="srst">{runIcon(run)}</span>}
      </div>
      {run?.diagnostic && run.stop !== "completed" && <p className="small errt">{run.diagnostic}</p>}
      <SubFeed sessionId={session} child={run?.child ?? ""} parent={parent} />
    </div>
  );
}

function TaskTab({ agent }: { agent: AgentView }) {
  const models = useStore((s) => s.models);
  const m = activeModel(models, agent);
  const chat = agent.kind === "chat";
  const act = async (a: "stop" | "pause" | "resume") => {
    try {
      await API.action(agent.id, a);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const cancel = async (id: string) => {
    try {
      await API.cancelInput(agent.id, id);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <>
      <div className="psec">{t("panel.overview")}</div>
      <div className="taskcard">
        <dl className="kv">
          <dt>{t("panel.status")}</dt>
          <dd>
            {/* waiting for an approval: one pill that says so, not 等你处理 + 等你批准 */}
            <span className={"pill " + pillTone(agent.status)}>{agent.status === "waiting" && agent.detail ? detailLabel(agent.detail) : statusLabel(agent.status)}</span>
            {agent.detail && agent.status !== "waiting" && <span className="small">{detailLabel(agent.detail)}</span>}
          </dd>
          <dt>{t("panel.model")}</dt>
          <dd>
            {m ? (
              <>
                <ProviderMark provider={m.provider} model={m.model} />
                <span className="ml">
                  {modelLabel(models, m)}
                  {m.effort ? ` · ${effortLabel(m.effort)}` : ""}
                </span>
              </>
            ) : (
              "—"
            )}
          </dd>
          {agent.kind !== "chat" && (
            <>
              <dt>{t("panel.mode")}</dt>
              <dd>
                {modeLabel(agent.effectiveMode)}
                {agent.effectiveMode === "host" && agent.modeUntil && <span className="small">{t("chat.modeUntil", { time: until(agent.modeUntil) }).replace(/^，/, "")}</span>}
              </dd>
              <dt>{t("panel.dir")}</dt>
              <dd className="mono" style={{ fontSize: 12 }}>
                {shortPath(agent.workspace)}
              </dd>
            </>
          )}
        </dl>
        {/* a plain chat has no queue to pause: just Stop, while it answers */}
        {(!chat || agent.status === "running" || agent.status === "waiting") && (
        <div className="acts">
          <button className="btn" type="button" disabled={agent.status !== "running" && agent.status !== "waiting"} onClick={() => act("stop")}>
            <Square size={12} />
            {t("panel.stop")}
          </button>
          {chat ? null : agent.paused ? (
            <button className="btn" type="button" onClick={() => act("resume")}>
              <Play size={12} />
              {t("panel.resume")}
            </button>
          ) : (
            <button className="btn" type="button" onClick={() => act("pause")}>
              <Pause size={12} />
              {t("panel.pause")}
            </button>
          )}
        </div>
        )}
      </div>
      {(!chat || agent.queue.length > 0) && (
        <div className="psec">{chat ? t("chat.queue", { n: agent.queue.length }) : t("panel.queue", { name: agent.name, n: agent.queue.length })}</div>
      )}
      {chat && agent.queue.length === 0 ? null : agent.queue.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("panel.queueEmpty")}
        </p>
      ) : (
        agent.queue.map((q, i) => (
          <div className="qi" key={q.id}>
            <span className="n">{i + 1}</span>
            <span className="g">
              <span className="qt">{q.text}</span>
              <span className="small" style={{ display: "block" }}>
                {i === 0 ? t("panel.queueNext") : ""}
                {q.model ? `${i === 0 ? " · " : ""}${modelLabel(models, q.model)}` : ""}
              </span>
            </span>
            <button className="ix" type="button" aria-label={t("panel.queueCancel")} title={t("panel.queueCancel")} onClick={() => cancel(q.id)}>
              <X size={13} />
            </button>
          </div>
        ))
      )}
      {!chat && <WorkTasks agent={agent} />}
    </>
  );
}

/** Models that can use provider-hosted web search / URL reading. */
export function supportsNative(r: ModelRef | null | undefined): boolean {
  if (!r) return false;
  return r.provider === "deepseek" || (r.provider === "magpie" && r.model.startsWith("deepseek/"));
}

function AgentTab({ agent, sessionId }: { agent: AgentView; sessionId: string }) {
  const models = useStore((s) => s.models);
  const events = useStore((s) => s.events[sessionId]);
  const ctx = useMemo(() => project(events ?? []).context[agent.id], [events, agent.id]);
  const [bd, setBd] = useState<ContextBreakdown | null>(null);
  const [compacting, setCompacting] = useState(false);
  const evCount = events?.length ?? 0;
  useEffect(() => {
    let live = true;
    API.context(agent.id, sessionId)
      .then((b) => live && setBd(b))
      .catch(() => live && setBd(null));
    return () => {
      live = false;
    };
  }, [agent.id, sessionId, Math.floor(evCount / 5)]);
  const [role, setRole] = useState(agent.role ?? "");
  const [order, setOrder] = useState<ModelRef[]>(agent.models ?? []);
  const [drag, setDrag] = useState<number | null>(null);
  const add = useMenu();
  useEffect(() => setRole(agent.role ?? ""), [agent.role]);
  useEffect(() => setOrder(agent.models ?? []), [agent.models]);
  const active = activeModel(models, agent);
  const used = bd?.used ?? ctx?.tokens ?? 0;
  const win = bd?.window || ctx?.window || 0;
  const pct = win ? Math.min(100, Math.round((used / win) * 100)) : null;
  const native = new Set(agent.nativeTools ?? []);
  const nativeOk = order.some((r) => supportsNative(r));
  const setNative = async (tool: string, on: boolean) => {
    const next = new Set(native);
    if (on) next.add(tool);
    else next.delete(tool);
    try {
      await API.patchAgent(agent.id, { nativeTools: [...next] });
      toast(t("panel.saved"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const compact = async () => {
    setCompacting(true);
    try {
      await API.compact(agent.id, sessionId);
      toast(t("ctx.compacted", { name: agent.name }));
      setBd(await API.context(agent.id, sessionId));
    } catch (e: any) {
      toast(t("ctx.compactFailed", { error: e?.message ?? String(e) }));
    } finally {
      setCompacting(false);
    }
  };

  const saveOrder = async (next: ModelRef[]) => {
    setOrder(next);
    try {
      await API.patchAgent(agent.id, { modelRefs: next.map((r) => refString(r)) });
      toast(t("panel.modelsSaved", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
      setOrder(agent.models ?? []);
    }
  };
  const move = (i: number, j: number) => {
    if (j < 0 || j >= order.length || i === j) return;
    const next = [...order];
    const [x] = next.splice(i, 1);
    next.splice(j, 0, x);
    void saveOrder(next);
  };
  const saveRole = async () => {
    if (role === (agent.role ?? "")) return;
    try {
      await API.patchAgent(agent.id, { role });
      toast(t("panel.saved"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const setMode = async (m: string) => {
    if (m === "host") {
      setState({ hostConfirm: agent.id });
      return;
    }
    try {
      await API.patchAgent(agent.id, { mode: m });
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const del = async () => {
    if (!(await confirmDialog(t("panel.deleteConfirm", { name: agent.name })))) return;
    try {
      await API.deleteAgent(agent.id);
      toast(t("panel.deleted", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const candidates = allModels(models).filter((m) => !order.some((r) => sameRef(r, m.ref)));
  const lookM = useMenu();

  return (
    <>
      <div className="aghead">
        {agent.kind === "chat" ? (
          <Avatar agent={agent} size={34} status />
        ) : (
          <button type="button" className="aglook" ref={lookM.ref} onClick={lookM.toggle} title={t("panel.changeLook")} aria-label={t("panel.changeLook")} aria-haspopup="dialog">
            <Avatar agent={agent} size={34} status />
          </button>
        )}
        <Popover anchor={lookM.anchor} open={lookM.open} onClose={lookM.close} width={300}>
          <LookPicker agent={agent} onChange={(p) => void API.patchAgent(agent.id, p).catch((e) => toast(e?.message ?? String(e)))} />
        </Popover>
        <div style={{ flex: 1, minWidth: 0 }}>
          <b>{agent.name}</b>
          <div className="small mono" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
            {agent.id}
          </div>
        </div>
      </div>
      {agent.kind === "chat" ? (
        <p className="small">{t("chat.blankNote")}</p>
      ) : (
        <div className="psec">{t("panel.role")}</div>
      )}
      {agent.kind !== "chat" && <Brief agent={agent} />}
      <div className="psec" hidden={agent.kind === "chat"}>
        {t("panel.roleTag")} <span style={{ fontWeight: 400 }}>{t("panel.roleTagHint")}</span>
      </div>
      <input hidden={agent.kind === "chat"} className="fi sm" value={role} placeholder={t("panel.rolePh")} onChange={(e) => setRole(e.target.value)} onBlur={saveRole} onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()} />

      <div className="psec">
        {t("panel.modelOrder")} <span style={{ fontWeight: 400 }}>{t("panel.modelOrderHint")}</span>
      </div>
      <div className="route">
        {order.map((r, i) => {
          const ex = isExhausted(models, r);
          const off = modelOff(models, r);
          return (
            <div
              key={refString(r) + i}
              className={"rt" + (drag === i ? " dragging" : "") + (off ? " off" : "")}
              draggable
              onDragStart={(e) => {
                setDrag(i);
                e.dataTransfer.effectAllowed = "move";
              }}
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault();
                if (drag !== null) move(drag, i);
                setDrag(null);
              }}
              onDragEnd={() => setDrag(null)}
            >
              <GripVertical size={13} className="grip" />
              <span className="n">{i + 1}</span>
              <ProviderMark provider={r.provider} model={r.model} />
              <span className="rtn">
                {modelLabel(models, r)}
                {r.effort && <span className="small"> · {effortLabel(r.effort)}</span>}
              </span>
              <span className="r">
                {off ? (
                  <span className="pill" title={t("set.modelOffHint")}>{t("set.modelOffSkipped")}</span>
                ) : ex ? (
                  <span className="pill red" title={exhaustedHint(ex)}>
                    {exhaustedLabel(ex)}
                  </span>
                ) : sameRef(active, r) ? (
                  <span className="pill ok">{t("panel.modelCurrent")}</span>
                ) : (
                  <span className="pill">{t("comp.modelBackup")}</span>
                )}
              </span>
              <span className="rtacts">
                <button className="ix" type="button" aria-label={t("panel.up")} title={t("panel.up")} disabled={i === 0} onClick={() => move(i, i - 1)}>
                  <ArrowUp size={13} />
                </button>
                <button className="ix" type="button" aria-label={t("panel.down")} title={t("panel.down")} disabled={i === order.length - 1} onClick={() => move(i, i + 1)}>
                  <ArrowDown size={13} />
                </button>
                <button className="ix" type="button" aria-label={t("panel.remove")} title={t("panel.remove")} onClick={() => saveOrder(order.filter((_, j) => j !== i))}>
                  <X size={13} />
                </button>
              </span>
            </div>
          );
        })}
        <button className="btn ghost addm" type="button" ref={add.ref} onClick={add.toggle} disabled={candidates.length === 0}>
          <Plus size={13} />
          {t("panel.addModel")}
        </button>
      </div>
      <Popover anchor={add.anchor} open={add.open} onClose={add.close} width={280}>
        {candidates.map((m) => (
          <button
            key={refString(m.ref)}
            className="mi"
            type="button"
            onClick={() => {
              add.close();
              void saveOrder([...order, m.ref]);
            }}
          >
            <ProviderMark provider={m.ref.provider} model={m.ref.model} size={16} />
            <span className="g">
              {m.info.name || m.info.id}
              <span>
                {m.provider.id} · {t(`kind.${m.provider.kind}` as Key)}
                {modelMeta(m.info) ? ` · ${modelMeta(m.info)}` : ""}
              </span>
            </span>
          </button>
        ))}
      </Popover>

      <div className="psec">{t("na.mode")}</div>
      <div className="modes">
        {(["read-only", "sandbox", "host"] as const).map((m) => (
          <button key={m} className={"mode" + (m === "host" ? " c" : "")} type="button" aria-pressed={agent.effectiveMode === m} onClick={() => setMode(m)}>
            <b>
              {modeLabel(m)}
              {m === "sandbox" && <span className="pill blue">{t("mode.recommended")}</span>}
            </b>
            <span>{t(`mode.${m}.desc` as Key)}</span>
          </button>
        ))}
      </div>

      {nativeOk && (
        <>
          <div className="psec">{t("panel.native")}</div>
          <div className="tglist">
            {(["web_search", "read_url"] as const).map((k) => (
              <div className="tgrow" key={k}>
                <Globe size={14} className="muted" />
                <span className="g">
                  <b>{t(`panel.native.${k}` as Key)}</b>
                  <span>{t(`panel.native.${k}.desc` as Key)}</span>
                </span>
                <button className="tg" type="button" role="switch" aria-checked={native.has(k)} aria-label={t(`panel.native.${k}` as Key)} onClick={() => setNative(k, !native.has(k))} />
              </div>
            ))}
          </div>
          <div className="small" style={{ marginTop: 6 }}>
            {t("panel.nativeNote")}
          </div>
        </>
      )}

      <AgentConnectors agent={agent} />

      <ToolLoading agent={agent} />

      <div className="psec">{t("panel.context")}</div>
      <div style={{ display: "flex", justifyContent: "space-between", fontSize: 12.5 }}>
        <span>{win ? `${kfmt(used)} / ${kfmt(win)}` : "—"}</span>
        <b>{pct === null ? "—" : pct + "%"}</b>
      </div>
      {bd ? (
        <div className="cbar" style={{ margin: "7px 0 6px" }}>
          {bd.segments
            .filter((g) => g.tokens > 0)
            .map((g) => (
              <i key={g.key} title={`${g.label} · ${kfmt(g.tokens)}`} style={{ flex: Math.max(g.tokens, win * 0.004), background: `var(--seg-${g.key === "free" ? "free" : g.key})` }} />
            ))}
          <span className="thr" style={{ left: Math.round((bd.threshold || 0.8) * 100) + "%" }} />
        </div>
      ) : (
        <div className="meter">
          <i className={pct !== null && pct >= 80 ? "amber" : ""} style={{ width: (pct ?? 0) + "%" }} />
          <span className="th" style={{ left: "80%" }} />
        </div>
      )}
      <div className="small">{t("panel.contextNote")}</div>
      <div className="acts sm">
        <button className="btn" type="button" disabled={compacting || !bd} onClick={compact}>
          {compacting ? <span className="spin sm" /> : <Minimize2 size={12} />}
          {t("ctx.compact")}
        </button>
      </div>

      {agent.kind !== "chat" && <div className="psec">{t("panel.instrMem")}</div>}
      <div className="tree" hidden={agent.kind === "chat"}>
        {(["INSTRUCTIONS.md", "MEMORY.md"] as const).map((f) => (
          <button
            key={f}
            className="tn"
            type="button"
            onClick={() => setState({ panelTab: "files", fileOpen: f })}
          >
            <Link2 size={13} />
            <span className="g">{f}</span>
            <span className="small">{t(f === "MEMORY.md" ? "panel.files.memory" : "panel.files.instructions")}</span>
          </button>
        ))}
      </div>
      <WorkNotes agent={agent} />
      <div style={{ marginTop: 20 }}>
        <button className="btn dng" type="button" onClick={del}>
          <Trash size={13} />
          {agent.kind === "chat" ? t("chat.delete") : t("panel.deleteAgent")}
        </button>
      </div>
    </>
  );
}

/** The instructions the agent works from: written from its name by a model
 *  right after creation, so while INSTRUCTIONS.md is still the placeholder
 *  the panel says so and checks again every few seconds. */
function Brief({ agent }: { agent: AgentView }) {
  const [text, setText] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const placeholder = "# " + agent.name + "\n\n" + (agent.role ?? "") + "\n";
  useEffect(() => {
    let live = true;
    let timer = 0;
    let tries = 0;
    const load = () =>
      API.file(agent.id, "INSTRUCTIONS.md")
        .then((f) => {
          if (!live) return;
          const c = f.content ?? "";
          setText(c);
          // still the placeholder: the brief is being written (each model gets 25 s)
          if (c === placeholder && tries++ < 60) timer = window.setTimeout(load, 3000);
        })
        .catch(() => live && setText(""));
    void load();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [agent.id, placeholder]);
  if (text === null) return null;
  const pending = text === placeholder;
  const body = text.replace(/^# .*\n+/, "").trim();
  return (
    <div className="pbrief">
      {pending ? (
        <div className="small pbrief-wait">
          <span className="spin sm" /> {t("panel.briefWriting")}
        </div>
      ) : body ? (
        <div className={"pbrief-body" + (open ? " open" : "")} onClick={() => setOpen(!open)} title={open ? undefined : t("panel.briefMore")}>
          {body}
        </div>
      ) : (
        <div className="small">{t("panel.briefEmpty")}</div>
      )}
      <button className="linkbtn" type="button" onClick={() => setState({ panelTab: "files", fileOpen: "INSTRUCTIONS.md" })}>
        {t("panel.briefEdit")}
      </button>
    </div>
  );
}

/** 工具加载: whether the agent's on-demand tools (MCP servers, connectors,
 *  heavy built-ins) go out with every request or are searched for, and
 *  which groups stay loaded from the start. */
function ToolLoading({ agent }: { agent: AgentView }) {
  const [groups, setGroups] = useState<{ name: string; count: number; summary: string }[] | null>(null);
  const [tokens, setTokens] = useState(0);
  useEffect(() => {
    let live = true;
    API.agentToolGroups(agent.id).then(
      (r) => {
        if (!live) return;
        setGroups((r.groups ?? []).filter((g) => g.deferred));
        setTokens(r.deferredTokens ?? 0);
      },
      () => live && setGroups([]),
    );
    return () => {
      live = false;
    };
  }, [agent.id]);
  if (!groups || groups.length === 0) return null; // nothing is ever held back
  const mode = agent.toolSearch || "auto";
  const always = new Set(agent.alwaysLoad ?? []);
  const patch = async (p: { toolSearch?: string; alwaysLoad?: string[] }) => {
    try {
      await API.patchAgent(agent.id, p);
      toast(t("panel.saved"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <>
      <div className="psec">{t("tl.title")}</div>
      <div className="seg tlseg" role="radiogroup" aria-label={t("tl.title")}>
        {(["auto", "never", "always"] as const).map((m) => (
          <button key={m} type="button" role="radio" aria-checked={mode === m} aria-pressed={mode === m} onClick={() => void patch({ toolSearch: m })}>
            {t(`tl.mode.${m}` as Key)}
          </button>
        ))}
      </div>
      <div className="small" style={{ marginTop: 6 }}>
        {t(`tl.desc.${mode}` as Key, { n: groups.reduce((a, g) => a + g.count, 0), k: kfmt(tokens) })}
      </div>
      {mode !== "never" && (
        <>
          <div className="small muted" style={{ margin: "10px 0 6px" }}>
            {t("tl.always")}
          </div>
          <div className="tglist tlgroups">
            {groups.map((g) => (
              <div className="tgrow" key={g.name}>
                <span className="g">
                  <b>
                    {groupLabel(g).name} <span className="muted">· {t("tl.count", { n: g.count })}</span>
                  </b>
                  <span>{groupLabel(g).desc}</span>
                </span>
                <button
                  className="tg"
                  type="button"
                  role="switch"
                  aria-checked={always.has(g.name)}
                  aria-label={g.name}
                  onClick={() => {
                    const next = new Set(always);
                    if (next.has(g.name)) next.delete(g.name);
                    else next.add(g.name);
                    void patch({ alwaysLoad: [...next] });
                  }}
                />
              </div>
            ))}
          </div>
        </>
      )}
    </>
  );
}

/** A tool group's name and line in the interface language (built-in groups
 *  are translated; an MCP server keeps its name). */
function groupLabel(g: { name: string; summary: string }): { name: string; desc: string } {
  const k = `tg.${g.name}` as Key;
  const v = t(k);
  if (v !== k) {
    const [name, desc] = v.split("|");
    return { name, desc: desc ?? g.summary };
  }
  const mcp = /^MCP server tools, e\.g\. (.*)$/.exec(g.summary);
  return { name: g.name, desc: mcp ? t("tg.mcp", { names: mcp[1] }) : g.summary };
}
