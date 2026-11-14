// Projects a session's event log into conversation feed items.

import type { ApprovalRequest, ModelRef, Part, SessionEvent, ToolCall, ToolResult } from "./api";

export interface Step {
  call: ToolCall;
  result?: ToolResult;
  time: string;
  /** An approval card is waiting for this call. */
  awaiting?: boolean;
}

export interface Decision {
  outcome: "allowed-once" | "rejected" | "cancelled" | "unavailable" | string;
  message?: string;
  remember?: boolean;
  by?: string;
}

export type FeedItem =
  | { k: "time"; id: string; time: string }
  | { k: "user"; id: string; text: string; from?: string; time: string }
  | {
      k: "agent";
      id: string;
      agent: string;
      text: string;
      thinking: string;
      model?: ModelRef;
      time: string;
      citations: Citation[];
      searches: string[];
      refusal: boolean;
    }
  | { k: "handover"; id: string; from: string; to: string; note: string; files: string[]; time: string }
  | { k: "owner"; id: string; owner: string; prev?: string; by?: string; reason: string }
  | { k: "chstate"; id: string; state: string; by?: string }
  | { k: "steps"; id: string; agent: string; steps: Step[] }
  | { k: "approval"; id: string; req: ApprovalRequest; decided?: Decision; call?: ToolCall; time: string }
  | { k: "switch"; id: string; agent: string; from: ModelRef; to: ModelRef; reason: string; detail?: string }
  | { k: "mode"; id: string; agent: string; to: string; until?: string }
  | { k: "compact"; id: string; summary: string; before: number; after: number }
  | { k: "sys"; id: string; tone: "info" | "error" | "muted"; text: string; key?: string; vars?: Record<string, string> }
  /** A plugin's event rendered by its conversation.node card. */
  | { k: "plugin"; id: string; type: string; data: any; time: string };

export interface Citation {
  title: string;
  url: string;
}

/** Inputs the daemon writes on a person's or agent's behalf in channels. */
const INJECTED = /^\[(handover|reassigned|resumed)\]/;

export interface Projection {
  items: FeedItem[];
  /** Last step/start numbers per agent. */
  context: Record<string, { tokens: number; window: number; model?: ModelRef }>;
  /** Last model used per agent (step/start or message/assistant). */
  lastModel: Record<string, ModelRef>;
  /** Agents whose turn is open (turn/start without turn/end). */
  openTurns: Set<string>;
}

const GAP_MS = 15 * 60 * 1000;

function textOf(parts: Part[] | null | undefined, type: string): string {
  return (parts ?? [])
    .filter((p) => p.type === type && p.text)
    .map((p) => p.text)
    .join("");
}

/** `cards` are event types plugins render (conversation.node keys). */
export function project(events: SessionEvent[], cards?: ReadonlySet<string>): Projection {
  const items: FeedItem[] = [];
  const context: Projection["context"] = {};
  const lastModel: Projection["lastModel"] = {};
  const openTurns = new Set<string>();
  const steps = new Map<string, Step>();
  const calls = new Map<string, ToolCall>();
  const approvals = new Map<string, Extract<FeedItem, { k: "approval" }>>();
  let group: Extract<FeedItem, { k: "steps" }> | null = null;
  let lastTime = 0;
  let seenState = false;

  const push = (it: FeedItem, time: string) => {
    const ts = new Date(time).getTime();
    if (!lastTime || ts - lastTime > GAP_MS) items.push({ k: "time", id: "t" + it.id, time });
    lastTime = ts;
    items.push(it);
  };

  for (const e of events) {
    const d = e.data ?? {};
    const id = String(e.seq);
    switch (e.type) {
      case "message/user":
        if (INJECTED.test(d.text ?? "")) break;
        group = null;
        push({ k: "user", id, text: d.text ?? "", from: d.from, time: e.time }, e.time);
        break;
      case "message/assistant": {
        const parts: Part[] = d.message?.parts ?? [];
        if (d.model?.provider) lastModel[e.agent ?? ""] = d.model;
        const text = textOf(parts, "text");
        const thinking = textOf(parts, "thinking");
        const citations: Citation[] = [];
        const seen = new Set<string>();
        for (const p of parts) {
          const url = p.meta?.url;
          if (p.type === "citation" && url && !seen.has(url)) {
            seen.add(url);
            citations.push({ title: p.text || url, url });
          }
        }
        const searches = parts.filter((p) => p.type === "server_tool" && p.text).map((p) => p.text as string);
        const refusal = d.stop === "refusal";
        if (text || thinking || citations.length || searches.length || refusal) {
          group = null;
          push({ k: "agent", id, agent: e.agent ?? "", text, thinking, model: d.model, time: e.time, citations, searches, refusal }, e.time);
        }
        break;
      }
      case "tool/call": {
        const call: ToolCall = d.call ?? { id: id, name: "?", args: null };
        calls.set(call.id, call);
        const st: Step = { call, time: e.time };
        steps.set(call.id, st);
        if (!group || group.agent !== (e.agent ?? "")) {
          group = { k: "steps", id, agent: e.agent ?? "", steps: [] };
          push(group, e.time);
        }
        group.steps.push(st);
        break;
      }
      case "tool/result": {
        const r: ToolResult = d;
        const st = steps.get(r.callId);
        if (st) st.result = r;
        const name = r.name ?? calls.get(r.callId)?.name;
        if (name && cards?.has("tool:" + name)) {
          let result: any;
          try {
            result = JSON.parse(r.content);
          } catch {
            result = { text: r.content };
          }
          push({ k: "plugin", id, type: "tool:" + name, data: { args: calls.get(r.callId)?.args ?? {}, result, isError: !!r.isError }, time: e.time }, e.time);
          group = null; // later steps start a new group under the card
        }
        break;
      }
      case "approval/asked": {
        group = null;
        const req: ApprovalRequest = { ...d, session: d.session ?? e.session, agent: d.agent ?? e.agent ?? "" };
        const it: Extract<FeedItem, { k: "approval" }> = { k: "approval", id, req, call: calls.get(req.callId), time: e.time };
        approvals.set(req.callId, it);
        const st = steps.get(req.callId);
        if (st) st.awaiting = true;
        push(it, e.time);
        break;
      }
      case "approval/decided": {
        const it = approvals.get(d.callId);
        const dec: Decision = { outcome: d.outcome, message: d.message, remember: d.remember, by: d.by };
        if (it) it.decided = dec;
        const st = steps.get(d.callId);
        if (st) st.awaiting = false;
        break;
      }
      case "model/switched":
        push({ k: "switch", id, agent: e.agent ?? "", from: d.from, to: d.to, reason: d.reason ?? "", detail: d.detail }, e.time);
        if (d.to) lastModel[e.agent ?? ""] = d.to;
        break;
      case "mode/changed":
        push({ k: "mode", id, agent: e.agent ?? "", to: d.to, until: d.until && !String(d.until).startsWith("0001") ? d.until : undefined }, e.time);
        break;
      case "compaction/summary":
        group = null;
        push({ k: "compact", id, summary: d.summary ?? "", before: d.tokensBefore ?? 0, after: d.tokensAfter ?? 0 }, e.time);
        break;
      case "turn/start":
        openTurns.add(e.agent ?? "");
        break;
      case "turn/end":
        openTurns.delete(e.agent ?? "");
        group = null;
        if (d.status === "failed") push({ k: "sys", id, tone: "error", text: d.error ?? "", key: "chat.turnFailed", vars: { error: d.error ?? "" } }, e.time);
        else if (d.status === "cancelled") push({ k: "sys", id, tone: "muted", text: "", key: "chat.turnCancelled" }, e.time);
        break;
      case "turn/error":
        push({ k: "sys", id, tone: "error", text: d.error ?? d.text ?? JSON.stringify(d) }, e.time);
        break;
      case "step/start":
        context[e.agent ?? ""] = { tokens: d.contextTokens ?? 0, window: d.contextWindow ?? 0, model: d.model };
        if (d.model?.provider) lastModel[e.agent ?? ""] = d.model;
        break;
      case "channel/handover":
        group = null;
        push({ k: "handover", id, from: d.from ?? e.agent ?? "", to: d.to ?? "", note: d.note ?? "", files: d.files ?? [], time: e.time }, e.time);
        break;
      case "channel/owner":
        // created: implied by the channel; handover: shown by channel/handover.
        if (d.reason === "assigned" || d.reason === "reassigned") {
          group = null;
          push({ k: "owner", id, owner: d.owner, prev: d.prev, by: d.by, reason: d.reason }, e.time);
        }
        break;
      case "channel/state":
        if (seenState) {
          group = null;
          const prev = items[items.length - 1];
          // Archive stops first: show one line, not "stopped" + "archived".
          if (d.state === "archived" && prev?.k === "chstate" && prev.state === "paused") items.pop();
          push({ k: "chstate", id, state: d.state, by: d.by }, e.time);
        }
        seenState = true;
        break;
      case "notice":
        push({ k: "sys", id, tone: "info", text: d.text ?? "" }, e.time);
        break;
      case "queue/paused":
        push({ k: "sys", id, tone: "muted", text: "", key: "chat.paused" }, e.time);
        break;
      case "queue/resumed":
        push({ k: "sys", id, tone: "muted", text: "", key: "chat.resumed" }, e.time);
        break;
      default:
        // session/created, session/meta, input/*, step/end, agent/inject, approval/policy: not shown
        if (cards?.has(e.type)) push({ k: "plugin", id, type: e.type, data: d, time: e.time }, e.time);
        break;
    }
  }
  return { items, context, lastModel, openTurns };
}

/** Last text message in the log (for list snippets). */
export function lastSnippet(events: SessionEvent[] | undefined): { text: string; time: string } | null {
  if (!events) return null;
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e.type === "message/user") return { text: e.data?.text ?? "", time: e.time };
    if (e.type === "message/assistant") {
      const t = textOf(e.data?.message?.parts, "text");
      if (t) return { text: t, time: e.time };
    }
  }
  return null;
}

/** Best-effort one-line rendering of tool arguments. */
export function argsPreview(args: any): string {
  if (args == null) return "";
  if (typeof args === "string") {
    try {
      args = JSON.parse(args);
    } catch {
      return args;
    }
  }
  if (typeof args !== "object") return String(args);
  for (const k of ["command", "cmd", "path", "url", "query", "file", "pattern"]) {
    if (typeof args[k] === "string") return args[k];
    if (Array.isArray(args[k])) return args[k].join(" ");
  }
  return JSON.stringify(args);
}

export function argsFull(args: any): string {
  if (args == null) return "";
  if (typeof args === "string") {
    try {
      return JSON.stringify(JSON.parse(args), null, 2);
    } catch {
      return args;
    }
  }
  return JSON.stringify(args, null, 2);
}
