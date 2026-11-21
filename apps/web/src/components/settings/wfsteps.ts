// Pure operations on a workflow's steps. A stage is one serial step; an
// agent stage with several agents runs them in parallel (the backend's
// "parallel" step). Everything maps 1:1 to Workflow.steps.

import type { WfStep } from "../../api";

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
export function agentsOf(s: WfStep): WfStep[] {
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
