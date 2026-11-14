import { useEffect, useMemo, useState } from "react";
import { ArrowDown, ArrowUp, FileText, GripVertical, Pause, Play, Plus, Square, Trash, X } from "lucide-react";
import { API, refString, type AgentView, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { activeModel, allModels, effortLabel, isExhausted, kfmt, modeLabel, modelLabel, pillTone, sameRef, statusLabel, until } from "../format";
import { project } from "../projection";
import { setState, toast, useStore, sessionAgent } from "../store";
import { Avatar } from "./Avatar";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";
import { shortPath } from "./Conversation";

export function Panel() {
  const current = useStore((s) => s.current);
  const tab = useStore((s) => s.panelTab);
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const agent = useMemo(() => sessionAgent(current), [current, agents, sessions]);
  if (!agent) return null;
  const tabs: [typeof tab, string][] = [
    ["task", t("panel.task")],
    ["files", t("panel.files")],
    ["agent", agent.name || t("panel.agent")],
  ];
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
        {tab === "task" && <TaskTab agent={agent} />}
        {tab === "files" && <FilesTab agent={agent} />}
        {tab === "agent" && <AgentTab key={agent.id} agent={agent} />}
      </div>
    </aside>
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
            {agent.detail && <span className="small">{agent.detail}</span>}
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
      <div className="psec">{t("panel.queue", { name: agent.name, n: agent.queue.length })}</div>
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

function FilesTab({ agent }: { agent: AgentView }) {
  return (
    <>
      <div className="psec mono">{shortPath(agent.workspace)}</div>
      <div className="tree">
        <div className="tn">
          <FileText size={14} />
          INSTRUCTIONS.md
        </div>
        <div className="tn">
          <FileText size={14} />
          MEMORY.md
        </div>
      </div>
      <div className="empty" style={{ marginTop: 12 }}>
        {t("panel.filesSoon")}
      </div>
    </>
  );
}

function AgentTab({ agent }: { agent: AgentView }) {
  const models = useStore((s) => s.models);
  const events = useStore((s) => s.events[agent.session]);
  const ctx = useMemo(() => project(events ?? []).context[agent.id], [events, agent.id]);
  const [role, setRole] = useState(agent.role ?? "");
  const [order, setOrder] = useState<ModelRef[]>(agent.models ?? []);
  const [drag, setDrag] = useState<number | null>(null);
  const add = useMenu();
  useEffect(() => setRole(agent.role ?? ""), [agent.role]);
  useEffect(() => setOrder(agent.models ?? []), [agent.models]);
  const active = activeModel(models, agent);
  const pct = ctx && ctx.window ? Math.min(100, Math.round((ctx.tokens / ctx.window) * 100)) : null;

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
      <div className="psec">{t("panel.role")}</div>
      <input className="fi sm" value={role} placeholder={t("panel.rolePh")} onChange={(e) => setRole(e.target.value)} onBlur={saveRole} onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()} />

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

      <div className="psec">{t("panel.context")}</div>
      <div style={{ display: "flex", justifyContent: "space-between", fontSize: 12.5 }}>
        <span>{ctx ? `${kfmt(ctx.tokens)} / ${kfmt(ctx.window)}` : "—"}</span>
        <b>{pct === null ? "—" : pct + "%"}</b>
      </div>
      <div className="meter">
        <i className={pct !== null && pct >= 80 ? "amber" : ""} style={{ width: (pct ?? 0) + "%" }} />
        <span className="th" style={{ left: "80%" }} />
      </div>
      <div className="small">{t("panel.contextNote")}</div>

      <div className="psec">{t("panel.instrMem")}</div>
      <dl className="kv">
        <dt>{t("panel.files.instructions")}</dt>
        <dd className="mono" style={{ fontSize: 12 }}>
          INSTRUCTIONS.md
        </dd>
        <dt>{t("panel.files.memory")}</dt>
        <dd className="mono" style={{ fontSize: 12 }}>
          MEMORY.md
        </dd>
      </dl>
      <div style={{ marginTop: 20 }}>
        <button className="btn dng" type="button" onClick={del}>
          <Trash size={13} />
          {t("panel.deleteAgent")}
        </button>
      </div>
    </>
  );
}
