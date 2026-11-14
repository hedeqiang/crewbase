// 工作流画布: the steps of a workflow as a left-to-right flow. A stage is
// one serial step; an agent stage with several agents runs them in
// parallel (the backend's "parallel" step). Nodes can be dragged onto a
// gap (a new serial stage there) or onto another agent stage (join it in
// parallel); "+" on a connector inserts a step; clicking a node edits it
// in the inspector. Everything maps 1:1 to Workflow.steps.

import { Bot, GripVertical, Hand, Play, Plus, Users, X } from "lucide-react";
import { useState, type DragEvent } from "react";
import type { WfStep } from "../../api";
import { t, type Key } from "../../i18n";
import { useStore } from "../../store";
import { Avatar } from "../Avatar";

// ---- pure operations on steps

/** Position of a node: stage index, and branch index for parallel stages. */
export interface NodeRef {
  stage: number;
  branch?: number;
}

function allIds(steps: WfStep[]): Set<string> {
  return new Set(steps.flatMap((s) => [s.id, ...(s.parallel ?? []).map((p) => p.id)]));
}

export function freshId(steps: WfStep[], base: string): string {
  const ids = allIds(steps);
  for (let i = 1; ; i++) if (!ids.has(base + i)) return base + i;
}

/** The agent nodes of a stage (one for an agent step, n for parallel). */
function agentsOf(s: WfStep): WfStep[] {
  if (s.kind === "parallel") return s.parallel ?? [];
  if (s.kind === "agent") return [s];
  return [];
}

/** Normalises a stage after edits: 1 branch → agent step, n → parallel. */
function stageOf(id: string, name: string | undefined, nodes: WfStep[]): WfStep {
  if (nodes.length === 1) {
    const n = nodes[0];
    return { ...n, id: n.id, kind: "agent", parallel: undefined };
  }
  return { id, name, kind: "parallel", parallel: nodes.map((n) => ({ ...n, kind: "agent" })) };
}

export function nodeAt(steps: WfStep[], r: NodeRef): WfStep | undefined {
  const s = steps[r.stage];
  if (!s) return undefined;
  return r.branch === undefined ? s : (s.parallel ?? [])[r.branch];
}

/** Removes a node; a parallel stage left with one branch becomes an agent step. */
export function removeNode(steps: WfStep[], r: NodeRef): WfStep[] {
  const s = steps[r.stage];
  if (!s) return steps;
  if (s.kind === "parallel" && r.branch !== undefined) {
    const rest = (s.parallel ?? []).filter((_, i) => i !== r.branch);
    if (rest.length === 0) return steps.filter((_, i) => i !== r.stage);
    return steps.map((x, i) => (i === r.stage ? stageOf(s.id, s.name, rest) : x));
  }
  return steps.filter((_, i) => i !== r.stage);
}

/** Inserts a new step of kind at stage index `at`. */
export function insertStep(steps: WfStep[], at: number, kind: "agent" | "approval" | "parallel"): { steps: WfStep[]; ref: NodeRef } {
  let st: WfStep;
  if (kind === "approval") st = { id: freshId(steps, "approve"), kind: "approval", approval: { summary: "" } };
  else if (kind === "parallel") {
    const id = freshId(steps, "group");
    const a = freshId(steps, "step");
    const b = freshId([...steps, { id: a, kind: "agent" }], "step");
    st = { id, kind: "parallel", parallel: [{ id: a, kind: "agent", agent: "", prompt: "{{prev}}" }, { id: b, kind: "agent", agent: "", prompt: "{{prev}}" }] };
  } else st = { id: freshId(steps, "step"), kind: "agent", agent: "", prompt: at === 0 ? "{{input}}" : "{{prev}}" };
  const out = [...steps.slice(0, at), st, ...steps.slice(at)];
  return { steps: out, ref: { stage: at, branch: kind === "parallel" ? 0 : undefined } };
}

/** Adds a parallel branch to an agent stage. */
export function addBranch(steps: WfStep[], stage: number): { steps: WfStep[]; ref: NodeRef } {
  const s = steps[stage];
  const nodes = agentsOf(s);
  const n: WfStep = { id: freshId(steps, "step"), kind: "agent", agent: "", prompt: nodes[0]?.prompt ?? "{{prev}}" };
  const out = steps.map((x, i) => (i === stage ? stageOf(s.kind === "parallel" ? s.id : freshId(steps, "group"), s.name, [...nodes, n]) : x));
  return { steps: out, ref: { stage, branch: nodes.length } };
}

/** Moves a node: to a gap (a new stage before index `gap`) or into an agent stage (parallel). */
export function moveNode(steps: WfStep[], from: NodeRef, to: { gap: number } | { join: number }): WfStep[] {
  const node = nodeAt(steps, from);
  if (!node) return steps;
  const src = steps[from.stage];
  const leavesStage = src.kind !== "parallel" || (src.parallel ?? []).length <= 1;
  let rest = removeNode(steps, from);
  const shift = (i: number) => (leavesStage && i > from.stage ? i - 1 : i);
  if ("gap" in to) {
    const at = shift(to.gap);
    const st: WfStep = node.kind === "approval" ? node : { ...node, kind: "agent" };
    return [...rest.slice(0, at), st, ...rest.slice(at)];
  }
  if (node.kind === "approval") return steps; // approvals cannot run in parallel
  const target = shift(to.join);
  const ts = rest[target];
  if (!ts || ts.kind === "approval") return steps;
  const nodes = [...agentsOf(ts), { ...node, kind: "agent" }];
  rest = rest.map((x, i) => (i === target ? stageOf(ts.kind === "parallel" ? ts.id : freshId(rest, "group"), ts.name, nodes) : x));
  return rest;
}

// ---- canvas

export function WorkflowCanvas({
  steps,
  trigger,
  selected,
  onSelect,
  onChange,
}: {
  steps: WfStep[];
  trigger: string;
  selected: NodeRef | null;
  onSelect: (r: NodeRef | null) => void;
  onChange: (steps: WfStep[], select?: NodeRef | null) => void;
}) {
  const agents = useStore((s) => s.agents);
  const [drag, setDrag] = useState<NodeRef | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const [menu, setMenu] = useState<number | null>(null);

  const start = (r: NodeRef) => (e: DragEvent) => {
    setDrag(r);
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", JSON.stringify(r));
  };
  const end = () => {
    setDrag(null);
    setOver(null);
  };
  const dropZone = (key: string, to: { gap: number } | { join: number }) => ({
    onDragOver: (e: DragEvent) => {
      if (!drag) return;
      e.preventDefault();
      setOver(key);
    },
    onDragLeave: () => setOver((o) => (o === key ? null : o)),
    onDrop: (e: DragEvent) => {
      e.preventDefault();
      if (drag) onChange(moveNode(steps, drag, to), null);
      end();
    },
  });
  const isSel = (r: NodeRef) => !!selected && selected.stage === r.stage && selected.branch === r.branch;
  const dragIsApproval = !!drag && nodeAt(steps, drag)?.kind === "approval";

  // render helpers (plain functions, not components: a component defined
  // here would remount on every render and cancel an ongoing drag)
  const gap = (at: number) => (
    <div key={"gap" + at} className={"wfgap" + (over === "g" + at ? " over" : "") + (drag ? " live" : "")} {...dropZone("g" + at, { gap: at })}>
      <span className="wfline" />
      <div className="wfins">
        <button type="button" className="wfplus" aria-label={t("wfc.insert")} title={t("wfc.insert")} onClick={() => setMenu(menu === at ? null : at)}>
          <Plus size={12} />
        </button>
        {menu === at && (
          <div className="wfmenu" role="menu">
            {(["agent", "parallel", "approval"] as const).map((k) => (
              <button
                key={k}
                type="button"
                role="menuitem"
                onClick={() => {
                  const r = insertStep(steps, at, k);
                  setMenu(null);
                  onChange(r.steps, r.ref);
                }}
              >
                {k === "agent" ? <Bot size={13} /> : k === "parallel" ? <Users size={13} /> : <Hand size={13} />}
                {t(`wfc.add.${k}` as Key)}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );

  const node = (s: WfStep, r: NodeRef, branch?: boolean) => {
    const a = s.agent ? agents[s.agent] : undefined;
    const approval = s.kind === "approval";
    return (
      <div
        key={r.stage + ":" + (r.branch ?? "") + ":" + s.id}
        className={"wfnode" + (approval ? " appr" : "") + (isSel(r) ? " sel" : "") + (drag && drag.stage === r.stage && drag.branch === r.branch ? " dragging" : "") + (!approval && !s.agent ? " empty" : "")}
        draggable
        onDragStart={start(r)}
        onDragEnd={end}
        role="button"
        tabIndex={0}
        aria-pressed={isSel(r)}
        onClick={() => onSelect(isSel(r) ? null : r)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onSelect(isSel(r) ? null : r);
          }
          if ((e.key === "Delete" || e.key === "Backspace") && steps.length > 1) {
            e.preventDefault();
            onChange(removeNode(steps, r), null);
          }
        }}
      >
        <span className="wfgrip" aria-hidden>
          <GripVertical size={12} />
        </span>
        <span className="wfico">{approval ? <Hand size={15} /> : a ? <Avatar agent={a} size={22} /> : <Bot size={15} />}</span>
        <span className="wfg">
          <b>{approval ? t("wfc.approval") : a?.name ?? t("wfc.pickAgent")}</b>
          <span className="mono">{s.id}</span>
          <span className="wfp">{approval ? s.approval?.summary || t("wfc.approvalDesc") : s.prompt}</span>
        </span>
        {branch !== undefined && (
          <button
            type="button"
            className="ix wfx"
            aria-label={t("panel.remove")}
            onClick={(e) => {
              e.stopPropagation();
              onChange(removeNode(steps, r), null);
            }}
          >
            <X size={11} />
          </button>
        )}
      </div>
    );
  };

  return (
    <div className="wfcol">
      <div className="wfcanvas" onClick={(e) => e.target === e.currentTarget && setMenu(null)}>
        <div className="wftrack">
          <div className="wfnode wftrig" aria-label={t("wfc.trigger")}>
            <span className="wfico">
              <Play size={14} />
            </span>
            <span className="wfg">
              <b>{t("wfc.trigger")}</b>
              <span className="wfp">{trigger}</span>
            </span>
          </div>
          {gap(0)}
          {steps.map((s, i) => {
            const branches = s.kind === "parallel" ? s.parallel ?? [] : null;
            const joinable = s.kind !== "approval" && !!drag && !dragIsApproval && drag.stage !== i;
            return (
              <div key={s.id + ":" + i} className="wfstagewrap">
                <div className={"wfstage" + (branches ? " par" : "") + (over === "j" + i ? " over" : "")} {...(joinable ? dropZone("j" + i, { join: i }) : {})}>
                  {branches ? (
                    <>
                      <span className="wfpar">
                        <Users size={11} /> {t("wfc.parallel", { n: branches.length })}
                      </span>
                      {branches.map((b, j) => node(b, { stage: i, branch: j }, true))}
                    </>
                  ) : (
                    node(s, { stage: i })
                  )}
                  {s.kind !== "approval" && (
                    <button
                      type="button"
                      className="wfbranch"
                      title={t("wfc.addBranch")}
                      onClick={() => {
                        const r = addBranch(steps, i);
                        onChange(r.steps, r.ref);
                      }}
                    >
                      <Plus size={11} /> {t("wfc.addBranch")}
                    </button>
                  )}
                  {joinable && <span className="wfjoinhint">{t("wfc.joinHint")}</span>}
                </div>
                {gap(i + 1)}
              </div>
            );
          })}
          <div className="wfnode wfend">
            <span className="wfg">
              <b>{t("wfc.end")}</b>
            </span>
          </div>
        </div>
      </div>
      <p className="small wfhint">{t("wfc.hint")}</p>
    </div>
  );
}
