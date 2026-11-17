// Projects a session's event log into conversation feed items.

import type { ApprovalRequest, Attachment, ModelRef, Part, SessionEvent, ToolCall, ToolResult } from "./api";

export interface Step {
  call: ToolCall;
  result?: ToolResult;
  time: string;
  /** An approval card is waiting for this call. */
  awaiting?: boolean;
  /** How the approval card for this call was answered (shown on the step). */
  decision?: Decision;
  /** delegate: the sub-agent run it started (its session) */
  sub?: string;
  /** computer_*: what it did in the app, from computer/action events */
  computer?: ComputerAction[];
}

/** One computer/action event (features/computertools ActionLog). */
export interface ComputerAction {
  app: string;
  appName?: string;
  action: string;
  element?: number;
  elementName?: string;
  keys?: string;
  menu?: string[];
  text?: string;
  foreground?: boolean;
  step?: number;
  steps?: number;
  ok: boolean;
  error?: string;
  /** workspace path of the screenshot the model asked for */
  screenshot?: string;
}

/** A sub-agent run started from this conversation (subagent/spawned). */
export interface SubRun {
  session: string;
  child: string;
  task: string;
  provider: string;
  started: string;
  finished?: string;
  stop?: string;
  diagnostic?: string;
}

export interface Decision {
  outcome: "allowed-once" | "rejected" | "cancelled" | "unavailable" | string;
  message?: string;
  remember?: boolean;
  by?: string;
}

/** Token counts of one model call, as recorded (core/llm Usage). */
export interface Usage {
  input: number;
  output: number;
  cacheRead?: number;
  cacheWrite?: number;
}

/** One model call of a turn: its model, usage and how long it took
 *  (step/start to message/assistant; unset when the log has no start). */
export interface TurnStep {
  model?: ModelRef;
  usage: Usage;
  ms?: number;
}

export type AgentItem = Extract<FeedItem, { k: "agent" }>;

/** A piece of a turn: a model message (narration when it came with tool
 *  calls) or a run of tool steps. */
export type TurnPart = { k: "msg"; it: AgentItem; narration: boolean } | { k: "steps"; id: string; steps: Step[] };

/** What a finished turn used, on the last reply of the turn. */
export interface TurnStats {
  steps: TurnStep[];
  status: string;
}

export type FeedItem = (
  | { k: "time"; id: string; time: string }
  | { k: "user"; id: string; seq: number; text: string; from?: string; time: string; to?: string; attachments?: Attachment[] }
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
      /** set on the turn's last reply once the turn ended */
      stats?: TurnStats;
      /** seq of the first user message of this reply's turn */
      turnFrom?: number;
    }
  | { k: "handover"; id: string; from: string; to: string; note: string; files: string[]; time: string }
  | { k: "owner"; id: string; owner: string; prev?: string; by?: string; reason: string }
  | { k: "chstate"; id: string; state: string; by?: string }
  | { k: "steps"; id: string; agent: string; steps: Step[] }
  /** One agent's turn (or the part of it after an interruption): what it
   *  said and did, in order, under one name. Text that came with a tool
   *  call is narration; the text the turn ended with is the answer. */
  | { k: "turn"; id: string; agent: string; time: string; parts: TurnPart[]; open: boolean }
  /** A result that came back to the agent on its own (a sub-agent's work,
   *  a background command, a follow-up): shown as a compact card, not as
   *  a message from the person. */
  | { k: "bg"; id: string; kind: BgKind; ok: boolean; who?: string; task?: string; title: string; body: string; time: string; sub?: string }
  | { k: "approval"; id: string; req: ApprovalRequest; decided?: Decision; call?: ToolCall; time: string }
  | { k: "switch"; id: string; agent: string; from: ModelRef; to: ModelRef; reason: string; detail?: string }
  | { k: "mode"; id: string; agent: string; to: string; until?: string }
  | { k: "compact"; id: string; summary: string; before: number; after: number }
  | { k: "sys"; id: string; tone: "info" | "error" | "muted"; text: string; key?: string; vars?: Record<string, string> }
  /** Images / a video an agent generated (features/media). */
  | {
      k: "media";
      id: string;
      agent: string;
      kind: "image" | "video";
      files: MediaFile[];
      prompt: string;
      model: string;
      call?: string;
      job?: string;
      /** pending: a video still being made */
      status: "pending" | "done" | "failed";
      spec?: { seconds?: number; aspect?: string; resolution?: string };
      error?: string;
      time: string;
    }
  /** A plugin's event rendered by its conversation.node card. */
  | { k: "plugin"; id: string; type: string; data: any; time: string }
) & {
  /** Hidden from the model by a later history/rewind (regenerate, edit):
   *  still shown, collapsed, so the record stays complete. */
  superseded?: Rewind;
};

/** A history/rewind: events with from <= seq < at no longer reach the
 *  agent's model (core/session RewindData). */
export interface Rewind {
  from: number;
  at: number;
  agent: string;
  reason: string;
}

export interface MediaFile {
  path: string;
  mime: string;
  size: number;
}

export interface Citation {
  title: string;
  url: string;
}

/** Inputs the daemon writes on a person's or agent's behalf in channels. */
// notes Crewbase writes to an agent as if from the user (handover, approvals decided while parked, …)
const INJECTED = /^\[(handover|reassigned|resumed|Approved|Not approved|Approval not given|Crewbase restarted)\]/;

export interface Projection {
  items: FeedItem[];
  /** Last step/start numbers per agent. */
  context: Record<string, { tokens: number; window: number; model?: ModelRef }>;
  /** Last model used per agent (step/start or message/assistant). */
  lastModel: Record<string, ModelRef>;
  /** Agents whose turn is open (turn/start without turn/end). */
  openTurns: Set<string>;
  /** Sub-agent runs started here, oldest first. */
  subruns: SubRun[];
  /** Per agent: the latest compaction's uptoSeq. History up to it is a
   *  summary now, so it cannot be resent from. */
  compactedUpto: Record<string, number>;
  /** Per agent: the last finished turn (its reply item and first user
   *  message), when nothing came after it: what regenerate resends. */
  lastTurn: Record<string, { item: string; from: number }>;
}

const GAP_MS = 15 * 60 * 1000;

function textOf(parts: Part[] | null | undefined, type: string): string {
  return (parts ?? [])
    .filter((p) => p.type === type && p.text)
    .map((p) => p.text)
    .join("");
}

export type BgKind = "delegation" | "job" | "followup";
const BG_FROM = new Set<string>(["delegation", "job", "followup"]);

/** Splits a background result's text ("[委派结果] d1（研究员）完成：\n\n…")
 *  into what the card shows. */
function parseBg(from: BgKind, text: string, tasks: Map<string, string>): Omit<Extract<FeedItem, { k: "bg" }>, "k" | "id" | "time"> {
  const nl = text.indexOf("\n");
  const first = (nl < 0 ? text : text.slice(0, nl)).trim();
  let body = (nl < 0 ? "" : text.slice(nl + 1)).trim();
  const head = first.replace(/^\[[^\]]*\]\s*/, "").replace(/[：:]\s*$/, "");
  if (from === "delegation") {
    // "<id>（<who>）<完成|没有完成>"
    const m = /^(\S+?)（([^）]*)）(.*)$/.exec(head);
    const id = m?.[1] ?? "";
    const who = m && m[2] !== "a sub-agent" ? m[2] : undefined;
    return { kind: from, ok: !/没有完成|failed|not/i.test(m?.[3] ?? head), who, task: tasks.get(id), title: head, body };
  }
  if (from === "followup") {
    // the note is the paragraph after the header; the instructions that follow are for the agent
    const note = body.split(/\n\s*\n/)[0]?.trim() ?? "";
    return { kind: from, ok: true, title: note || head, body: "" };
  }
  return { kind: from, ok: !/失败|failed/i.test(first), title: head, body };
}

/** `cards` are event types plugins render (conversation.node keys). */
export function project(events: SessionEvent[], cards?: ReadonlySet<string>): Projection {
  const items: FeedItem[] = [];
  const context: Projection["context"] = {};
  const lastModel: Projection["lastModel"] = {};
  const openTurns = new Set<string>();
  const steps = new Map<string, Step>();
  const calls = new Map<string, ToolCall>();
  const delegated = new Map<string, string>(); // delegation id → its task
  const subruns: SubRun[] = [];
  const subOf = new Map<string, SubRun>(); // child session →
  const pendingDelegate: Step[] = []; // delegate calls not yet matched to a run
  const runForTask = (task?: string) => {
    if (!task) return undefined;
    const k = task.trim().slice(0, 60);
    for (let i = subruns.length - 1; i >= 0; i--) if (subruns[i].task.trim().slice(0, 60) === k) return subruns[i];
    return undefined;
  };
  const approvals = new Map<string, Extract<FeedItem, { k: "approval" }>>();
  const videoJobs = new Map<string, Extract<FeedItem, { k: "media" }>>();
  let lastMedia: Extract<FeedItem, { k: "media" }> | null = null;

  let lastTime = 0;
  let seenState = false;

  // regenerate / edit: what a later history/rewind took back from the model
  const rewinds: Rewind[] = [];
  for (const e of events)
    if (e.type === "history/rewind" && e.data?.from > 0 && e.data.from < e.seq)
      rewinds.push({ from: e.data.from, at: e.seq, agent: e.agent ?? "", reason: e.data.reason ?? "" });
  const compactedUpto: Record<string, number> = {};
  const lastTurn: Projection["lastTurn"] = {};
  // the open turn per agent: its first user message, model calls, last reply
  type Open = { from?: number; steps: TurnStep[]; last?: Extract<FeedItem, { k: "agent" }>; stepAt?: number };
  const turns = new Map<string, Open>();
  const pendingFrom = new Map<string, number>(); // agent → first user message since its last turn

  // the open turn item per agent; content of that agent goes into it
  const cur = new Map<string, Extract<FeedItem, { k: "turn" }>>();
  const push = (it: FeedItem, time: string) => {
    const ts = new Date(time).getTime();
    // the widest rewind covering it: an edit further back takes in an
    // earlier regenerate, and shows as one run
    let sup: Rewind | undefined;
    for (const r of rewinds) if (Number(it.id) >= r.from && Number(it.id) < r.at && (!sup || r.from < sup.from || (r.from === sup.from && r.at > sup.at))) sup = r;
    if (sup) it.superseded = sup;
    if (!lastTime || ts - lastTime > GAP_MS) items.push({ k: "time", id: "t" + it.id, time, superseded: sup });
    lastTime = ts;
    items.push(it);
    // a line or card between: the agent's next output starts a new item
    // (a pending approval card is the exception: it leaves once answered)
    if (it.k !== "turn" && it.k !== "approval") {
      const ag = (it as { agent?: string }).agent;
      if (ag) cur.delete(ag);
      else cur.clear();
    }
  };

  const turnFor = (agent: string, id: string, time: string) => {
    let t = cur.get(agent);
    if (!t) {
      t = { k: "turn", id, agent, time, parts: [], open: true };
      cur.set(agent, t);
      push(t, time);
    }
    return t;
  };
  // something else came into the conversation: an agent's later output
  // starts a new item below it rather than growing one above it
  const breakTurns = (agent?: string) => {
    if (agent === undefined) cur.clear();
    else cur.delete(agent);
  };
  for (const e of events) {
    const d = e.data ?? {};
    const id = String(e.seq);
    switch (e.type) {
      case "message/user":
        for (const k of Object.keys(lastTurn)) delete lastTurn[k];
        // a message joined into a running turn belongs to that turn
        for (const to of [d.to ?? "", ...((d.also as string[] | undefined) ?? [])]) if (!pendingFrom.has(to) && !turns.has(to)) pendingFrom.set(to, e.seq);
        if (INJECTED.test(d.text ?? "")) break;
        breakTurns();
        if (BG_FROM.has(d.from ?? "") && /^\[[^\]]+\]/.test(d.text ?? "")) {
          const bg = parseBg(d.from as BgKind, d.text ?? "", delegated);
          push({ k: "bg", id, time: e.time, ...bg, sub: bg.kind === "delegation" ? runForTask(bg.task)?.session : undefined }, e.time);
          break;
        }
        push({ k: "user", id, seq: e.seq, text: d.text ?? "", from: d.from, time: e.time, to: d.to, attachments: d.attachments }, e.time);
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
        const turn = turns.get(e.agent ?? "");
        if (turn) {
          const ts = new Date(e.time).getTime();
          const u = d.usage ?? {};
          turn.steps.push({
            model: d.model?.provider ? d.model : undefined,
            usage: { input: u.input ?? 0, output: u.output ?? 0, cacheRead: u.cacheRead, cacheWrite: u.cacheWrite },
            ms: turn.stepAt !== undefined && ts >= turn.stepAt ? ts - turn.stepAt : undefined,
          });
          turn.stepAt = undefined;
        }
        if (text || thinking || citations.length || searches.length || refusal) {
          const it: AgentItem = { k: "agent", id, agent: e.agent ?? "", text, thinking, model: d.model, time: e.time, citations, searches, refusal };
          if (turn) turn.last = it;
          const narration = parts.some((p) => p.type === "tool_call");
          turnFor(e.agent ?? "", id, e.time).parts.push({ k: "msg", it, narration });
        }
        break;
      }
      case "subagent/spawned": {
        const r: SubRun = { session: d.childSession, child: d.child, task: d.task ?? "", provider: d.provider ?? "", started: e.time };
        if (r.session) {
          subruns.push(r);
          subOf.set(r.session, r);
          // the delegate call that started it: the oldest one not matched yet
          const st = pendingDelegate.shift();
          if (st) st.sub = r.session;
        }
        break;
      }
      case "subagent/finished": {
        const r = subOf.get(d.childSession);
        if (r) Object.assign(r, { finished: e.time, stop: d.stopReason, diagnostic: d.diagnostic });
        break;
      }
      case "tool/call": {
        const call: ToolCall = d.call ?? { id: id, name: "?", args: null };
        calls.set(call.id, call);
        const st: Step = { call, time: e.time };
        steps.set(call.id, st);
        if (call.name === "delegate") pendingDelegate.push(st);
        const tt = turnFor(e.agent ?? "", id, e.time);
        const lastPart = tt.parts[tt.parts.length - 1];
        if (lastPart?.k === "steps") lastPart.steps.push(st);
        else tt.parts.push({ k: "steps", id, steps: [st] });
        break;
      }
      case "tool/result": {
        const r: ToolResult = d;
        const st = steps.get(r.callId);
        if (st) st.result = r;
        const name = r.name ?? calls.get(r.callId)?.name;
        // a background delegation: remember its task for the result card
        const dm = /Delegated as (\S+?)\b/.exec(r.content ?? "");
        if (dm && name === "delegate") {
          const a = calls.get(r.callId)?.args;
          const task = typeof a === "string" ? (() => { try { return JSON.parse(a).task; } catch { return ""; } })() : a?.task;
          if (task) delegated.set(dm[1], String(task));
        }
        if (name && cards?.has("tool:" + name)) {
          let result: any;
          try {
            result = JSON.parse(r.content);
          } catch {
            result = { text: r.content };
          }
          push({ k: "plugin", id, type: "tool:" + name, data: { args: calls.get(r.callId)?.args ?? {}, result, isError: !!r.isError }, time: e.time }, e.time);
        }
        break;
      }
      case "approval/asked": {
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
        if (st) {
          st.awaiting = false;
          st.decision = dec;
          // the answer shows on the tool step itself; the card leaves the feed
          if (it) {
            const at = items.indexOf(it);
            if (at >= 0) items.splice(at, 1);
          }
        }
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
        push({ k: "compact", id, summary: d.summary ?? "", before: d.tokensBefore ?? 0, after: d.tokensAfter ?? 0 }, e.time);
        // the footer's context figure follows the compaction until the next step
        if (d.tokensAfter && context[e.agent ?? ""]) context[e.agent ?? ""] = { ...context[e.agent ?? ""], tokens: d.tokensAfter };
        if (d.uptoSeq) compactedUpto[e.agent ?? ""] = d.uptoSeq;
        break;
      case "turn/start": {
        const ag = e.agent ?? "";
        breakTurns(ag);
        openTurns.add(ag);
        delete lastTurn[ag];
        turns.set(ag, { from: pendingFrom.get(ag) ?? pendingFrom.get(""), steps: [] });
        pendingFrom.delete(ag);
        pendingFrom.delete("");
        break;
      }
      case "turn/end": {
        const ag = e.agent ?? "";
        openTurns.delete(ag);
        const ti = cur.get(ag);
        if (ti) ti.open = false;
        breakTurns(ag);
        const turn = turns.get(ag);
        turns.delete(ag);
        if (turn?.last) {
          turn.last.stats = { steps: turn.steps, status: d.status ?? "" };
          turn.last.turnFrom = turn.from;
          if (turn.from !== undefined) lastTurn[ag] = { item: turn.last.id, from: turn.from };
        }
        if (d.status === "failed") {
          push({ k: "sys", id, tone: "error", text: d.error ?? "", key: "chat.turnFailed", vars: { error: d.error ?? "" } }, e.time);
          // a failed turn can be retried from its first message (when it has
          // no reply to regenerate, the error row offers it)
          if (turn?.from !== undefined && !turn.last) lastTurn[ag] = { item: id, from: turn.from };
        }
        else if (d.status === "cancelled") push({ k: "sys", id, tone: "muted", text: "", key: "chat.turnCancelled" }, e.time);
        break;
      }
      case "turn/error":
        push({ k: "sys", id, tone: "error", text: d.error ?? d.text ?? JSON.stringify(d) }, e.time);
        break;
      case "step/start":
        context[e.agent ?? ""] = { tokens: d.contextTokens ?? 0, window: d.contextWindow ?? 0, model: d.model };
        if (d.model?.provider) lastModel[e.agent ?? ""] = d.model;
        {
          const turn = turns.get(e.agent ?? "");
          if (turn) turn.stepAt = new Date(e.time).getTime();
        }
        break;
      case "channel/handover":
        push({ k: "handover", id, from: d.from ?? e.agent ?? "", to: d.to ?? "", note: d.note ?? "", files: d.files ?? [], time: e.time }, e.time);
        break;
      case "channel/owner":
        // created: implied by the channel; handover: shown by channel/handover.
        if (d.reason === "assigned" || d.reason === "reassigned") {
          push({ k: "owner", id, owner: d.owner, prev: d.prev, by: d.by, reason: d.reason }, e.time);
        }
        break;
      case "channel/state":
        if (seenState) {
          const prev = items[items.length - 1];
          // Archive stops first: show one line, not "stopped" + "archived".
          if (d.state === "archived" && prev?.k === "chstate" && prev.state === "paused") items.pop();
          push({ k: "chstate", id, state: d.state, by: d.by }, e.time);
        }
        seenState = true;
        break;
      case "computer/action": {
        const st = d.call ? steps.get(d.call) : undefined;
        if (st) (st.computer ??= []).push(d as ComputerAction);
        break;
      }
      case "media/generated": {
        const f: MediaFile = { path: d.path ?? "", mime: d.mime ?? "", size: d.size ?? 0 };
        const job = d.job ? videoJobs.get(d.job) : undefined;
        if (job) {
          job.files = [f];
          job.status = "done";
          break;
        }
        // several images of one call show together
        if (lastMedia && lastMedia.kind === "image" && d.kind === "image" && d.call && lastMedia.call === d.call && items[items.length - 1] === lastMedia) {
          lastMedia.files.push(f);
          break;
        }
        lastMedia = { k: "media", id, agent: e.agent ?? "", kind: d.kind === "video" ? "video" : "image", files: [f], prompt: d.prompt ?? "", model: d.model ?? "", call: d.call, job: d.job, status: "done", time: e.time };
        push(lastMedia, e.time);
        break;
      }
      case "media/video-job": {
        const it = videoJobs.get(d.job);
        if (d.status === "started" && !it) {
          const m: Extract<FeedItem, { k: "media" }> = {
            k: "media",
            id,
            agent: e.agent ?? "",
            kind: "video",
            files: [],
            prompt: d.prompt ?? "",
            model: d.model ?? "",
            job: d.job,
            status: "pending",
            spec: { seconds: d.seconds, aspect: d.aspect, resolution: d.resolution },
            time: e.time,
          };
          videoJobs.set(d.job, m);
          push(m, e.time);
        } else if (it && d.status === "failed") {
          it.status = "failed";
          it.error = d.error ?? "";
        } else if (it && d.status === "done" && it.files.length) {
          it.status = "done";
        }
        break;
      }
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
  return { items, context, lastModel, openTurns, subruns, compactedUpto, lastTurn };
}

/** Last text message in the log (for list snippets). */
export function lastSnippet(events: SessionEvent[] | undefined): { text: string; time: string } | null {
  if (!events) return null;
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i];
    if (e.type === "message/user" && !INJECTED.test(e.data?.text ?? "")) return { text: e.data?.text ?? "", time: e.time };
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
  for (const k of ["command", "cmd", "task", "prompt", "path", "url", "query", "file", "pattern", "text", "note"]) {
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
