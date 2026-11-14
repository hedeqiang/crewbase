import { useEffect, useMemo, useState } from "react";
import { Archive, ArrowDown, ArrowUp, Globe, GripVertical, Link2, Minimize2, Pause, Play, Plus, Shuffle, Square, Trash, UserPlus, X } from "lucide-react";
import { API, refString, type AgentView, type ChannelView, type ContextBreakdown, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { activeModel, allModels, detailLabel, effortLabel, isExhausted, kfmt, modeLabel, modelLabel, pillTone, sameRef, statusLabel, until } from "../format";
import { project } from "../projection";
import { refreshChannel, setState, toast, useStore, sessionAgent } from "../store";
import { FilesTab } from "./Files";
import { Avatar } from "./Avatar";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";
import { shortPath } from "./Conversation";
import { PanelView, useSlot } from "./UISlots";

export function Panel() {
  const current = useStore((s) => s.current);
  const tab = useStore((s) => s.panelTab);
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const channels = useStore((s) => s.channels);
  const agent = useMemo(() => sessionAgent(current), [current, agents, sessions, channels]);
  const channel = current ? channels[current] : undefined;
  const pluginTabs = useSlot("sidebar-right.tab");
  if (!agent) return null;
  const tabs: [typeof tab, string][] = [
    ["task", t("panel.task")],
    ["files", t("panel.files")],
    ["agent", agent.kind === "chat" ? t("chat.settingsTab") : agent.name || t("panel.agent")],
    ...pluginTabs.map((c): [typeof tab, string] => [`ui:${c.key}`, c.title || c.key]),
  ];
  const pluginTab = tab.startsWith("ui:") ? pluginTabs.find((c) => "ui:" + c.key === tab) : undefined;
  return (
    <aside className="panel">
      <div className="ptabs" role="tablist">
        {tabs.map(([k, l]) => (
          <button key={k} className="ptab" type="button" role="tab" aria-selected={tab === k} onClick={() => setState({ panelTab: k })}>
            {l}
          </button>
        ))}
        <span style={{ flex: 1 }} />
        <button className="ix" type="button" aria-label={t("common.close")} onClick={() => setState({ panelOpen: false })}>
          <X size={13} />
        </button>
      </div>
      <div className="pbody">
        {tab === "task" && (channel ? <ChannelTask key={channel.id} channel={channel} /> : <TaskTab agent={agent} />)}
        {tab === "files" && <FilesTab agent={agent} />}
        {tab === "agent" && <AgentTab key={agent.id} agent={agent} sessionId={current ?? agent.session} />}
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
            <button className="btn" type="button" disabled={busy || archived} onClick={() => run(() => API.channelAction(channel.id, "stop"), t("ch.stoppedToast"))}>
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
            onClick={() => window.confirm(t("ch.archiveConfirm", { name: channel.title || channel.id })) && run(() => API.channelAction(channel.id, "archive"), t("ch.archivedToast"))}
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
        <button className="btn ghost addm" type="button" ref={addM.ref} onClick={addM.toggle} disabled={others.length === 0} style={{ marginTop: 6 }}>
          <UserPlus size={13} />
          {t("ch.addMember")}
        </button>
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

function TaskTab({ agent }: { agent: AgentView }) {
  const models = useStore((s) => s.models);
  const m = activeModel(models, agent);
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
      <div className="psec">{t("panel.status")}</div>
      <div className="taskcard">
        <dl className="kv">
          <dt>{t("panel.status")}</dt>
          <dd>
            <span className={"pill " + pillTone(agent.status)}>{statusLabel(agent.status)}</span>
            {agent.detail && <span className="small">{detailLabel(agent.detail)}</span>}
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
          <dt>{t("panel.mode")}</dt>
          <dd>
            {modeLabel(agent.effectiveMode)}
            {agent.effectiveMode === "host" && agent.modeUntil && <span className="small">{t("chat.modeUntil", { time: until(agent.modeUntil) }).replace(/^，/, "")}</span>}
          </dd>
          <dt>{t("panel.dir")}</dt>
          <dd className="mono" style={{ fontSize: 12 }}>
            {shortPath(agent.workspace)}
          </dd>
        </dl>
        <div className="acts">
          <button className="btn" type="button" disabled={agent.status !== "running" && agent.status !== "waiting"} onClick={() => act("stop")}>
            <Square size={12} />
            {t("panel.stop")}
          </button>
          {agent.paused ? (
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
      </div>
      <div className="psec">{agent.kind === "chat" ? t("chat.queue", { n: agent.queue.length }) : t("panel.queue", { name: agent.name, n: agent.queue.length })}</div>
      {agent.queue.length === 0 ? (
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
    if (!window.confirm(t("panel.deleteConfirm", { name: agent.name }))) return;
    try {
      await API.deleteAgent(agent.id);
      toast(t("panel.deleted", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const candidates = allModels(models).filter((m) => !order.some((r) => sameRef(r, m.ref)));

  return (
    <>
      <div className="aghead">
        <Avatar agent={agent} size={34} status />
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
      <input hidden={agent.kind === "chat"} className="fi sm" value={role} placeholder={t("panel.rolePh")} onChange={(e) => setRole(e.target.value)} onBlur={saveRole} onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()} />

      <div className="psec">
        {t("panel.modelOrder")} <span style={{ fontWeight: 400 }}>{t("panel.modelOrderHint")}</span>
      </div>
      <div className="route">
        {order.map((r, i) => {
          const ex = isExhausted(models, r);
          return (
            <div
              key={refString(r) + i}
              className={"rt" + (drag === i ? " dragging" : "")}
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
                {ex ? (
                  <span className="pill red">{t("comp.modelExhausted")}</span>
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
      <div style={{ marginTop: 20 }}>
        <button className="btn dng" type="button" onClick={del}>
          <Trash size={13} />
          {agent.kind === "chat" ? t("chat.delete") : t("panel.deleteAgent")}
        </button>
      </div>
    </>
  );
}
