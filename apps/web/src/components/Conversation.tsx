import { Welcome } from "./Welcome";
import { runShortcut } from "../actions";
import { keyLabel } from "../shortcuts";
import { catLabel } from "./Settings";
import type { Draft } from "./NewAgent";
import { createContext, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useCopy } from "../clipboard";
import {
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  ChevronLeft,
  CornerLeftUp,
  CircleX,
  Terminal,
  AlarmClock,
  Users,
  CircleMinus,
  Folder,
  FolderOpen,
  Clock,
  Undo2,
  Hand,
  Mic,
  Minimize2,
  Monitor,
  Plus,
  Shield,
  ShieldCheck,
  ShieldX,
  Wrench,
  ArrowRightLeft,
  Search as SearchIcon,
  Link2,
  Info,
  UserRound,
  Pause,
  Archive,
  Play,
  MicOff,
  Volume2,
  Square,
  Copy,
  Pencil,
  RefreshCw,
  History,
} from "lucide-react";
import { display, enterSends, MAC } from "../shortcuts";
import { API, desktop, parseRef, refString, useRawURL, type AgentView, type ChannelView, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { argsFull, argsPreview, project, type AgentItem, type Decision, type FeedItem, type Rewind, type Step, type TurnPart, type TurnStats } from "../projection";
import { hasModels, loadSession, setModelOverride, openConnect, openSubRun, setState, toast, useStore, sessionAgent } from "../store";
import { TakeoverCard, takeoverReason } from "./Browser";
import { cloudVoice, dictate, dictationSupported, primeVoice, speak, speaking, speakSupported } from "../voice";
import { addFiles, clearFiles, filesFrom, MsgAttachments, PendingChips } from "./Attachments";
import { MediaCard } from "./Media";
import { LocalPreviewCards, PreviewLine } from "./Preview";
import {
  activeModel,
  ago,
  detailLabel,
  dayLabel,
  findProvider,
  hhmm,
  kfmt,
  modeLabel,
  modelLabel,
  shortModelEffort,
  replyUsage,
  tokfmt,
  costfmt,
  until,
  NO_MODEL,
  MODEL_UNAVAILABLE,
  providerName,
} from "../format";
import { Avatar, channelColor, colorFor, ink } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { Markdown } from "./Markdown";
import { LinkCards } from "./LinkCards";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";
import { ContextMap } from "./ContextMap";
import { ModelPicker } from "./ModelPicker";
import { PluginCard, SlotIcon, runCommand, useSlot } from "./UISlots";

export function Conversation({ onTemplate }: { onTemplate?: (d: Partial<Draft>) => void } = {}) {
  const current = useStore((s) => s.current);
  const sessions = useStore((s) => s.sessions);
  const agents = useStore((s) => s.agents);
  const channels = useStore((s) => s.channels);
  useEffect(() => {
    if (current) void loadSession(current);
  }, [current]);
  const agent = useMemo(() => sessionAgent(current), [current, sessions, agents, channels]);
  const channel = current ? channels[current] : undefined;

  if (!current || !agent)
    return (
      <section className="main">
        <Welcome onTemplate={onTemplate ?? (() => {})} />
      </section>
    );
  return <ConversationView key={current} sessionId={current} agent={agent} channel={channel} />;
}

function ConversationView({ sessionId, agent, channel }: { sessionId: string; agent: AgentView; channel?: ChannelView }) {
  const events = useStore((s) => s.events[sessionId]);
  const err = useStore((s) => s.loadErr[sessionId]);
  const nodeSlots = useSlot("conversation.node");
  const cardTypes = useMemo(() => new Set(nodeSlots.map((c) => c.key)), [nodeSlots]);
  const proj = useMemo(() => project(events ?? [], cardTypes), [events, cardTypes]);
  const ctx = proj.context[agent.id];
  const kind = useStore((s) => s.sessions[sessionId]?.kind);
  const override = useStore((s) => s.overrides[agent.id] ?? null);
  const oneToOne = !channel && kind === "agent";
  const idle = agent.status !== "running" && agent.status !== "waiting" && !agent.paused && agent.queue.length === 0;
  const actions = useMemo<MsgActions | null>(
    () =>
      oneToOne
        ? {
            agent,
            idle,
            compactedUpto: proj.compactedUpto[agent.id] ?? 0,
            lastTurn: proj.lastTurn[agent.id],
            resend: async (seq, text) => {
              try {
                await API.resend(agent.id, { session: sessionId, seq, text, model: override ?? undefined });
                return true;
              } catch (e: any) {
                toast(t("msg.resendFailed", { error: e?.message ?? String(e) }));
                return false;
              }
            },
          }
        : null,
    [oneToOne, agent, idle, proj, sessionId, override],
  );
  return (
    <section className="main">
      <MsgCtx.Provider value={actions}>
        <Feed sessionId={sessionId} agent={agent} channel={channel} items={proj.items} working={proj.openTurns} loading={!events} error={err} />
      </MsgCtx.Provider>
      <Composer sessionId={sessionId} agent={agent} channel={channel} ctx={ctx} lastModel={proj.lastModel[agent.id]} />
    </section>
  );
}

// ---------------------------------------------------------------- message actions

/** Regenerate / edit in a one-to-one conversation (null elsewhere: channels
 *  and sub-agent runs offer copy only). */
interface MsgActions {
  agent: AgentView;
  /** the agent is not working and has nothing queued */
  idle: boolean;
  /** history up to this seq is a compaction summary: no resending from it */
  compactedUpto: number;
  /** the last finished turn, when nothing came after it */
  lastTurn?: { item: string; from: number };
  /** regenerate (no text) or edit and resend from the user message seq */
  resend: (seq: number, text?: string) => Promise<boolean>;
}

const MsgCtx = createContext<MsgActions | null>(null);
/** The session the feed shows (for its live streams). */
const SessionCtx = createContext("");

function CopyBtn({ text }: { text: string }) {
  const { copied, copy } = useCopy();
  const label = copied ? t("msg.copied") : t("msg.copy");
  return (
    <button type="button" className={"ix mact" + (copied ? " on" : "")} aria-label={label} title={label} onClick={() => void copy(text).then((ok) => ok || toast(t("msg.copyFailed")))}>
      {copied ? <Check size={14} /> : <Copy size={14} />}
    </button>
  );
}

/** Model · speed on the left; tokens and cost of the whole turn on the right. */
function ReplyStats({ stats }: { stats: TurnStats }) {
  const models = useStore((s) => s.models);
  const u = replyUsage(models, stats);
  const total = u.input + u.output;
  const name = u.model ? shortModelEffort(models, u.model) : null;
  const tokTitle = t("msg.tokensTitle", {
    calls: u.calls,
    input: u.input.toLocaleString(),
    cache: u.cacheRead ? t("msg.cacheRead", { n: u.cacheRead.toLocaleString() }) : "",
    output: u.output.toLocaleString(),
  });
  return (
    <div className="replystats">
      <span className="rs-l">
        {name && u.model && (
          <span className="rs-model" title={modelLabel(models, u.model)}>
            {name.name}
            {name.effort && <span className="effs">{name.effort}</span>}
          </span>
        )}
        {u.speed !== undefined && (
          <span title={t("msg.speedTitle")}>
            {name ? " · " : ""}
            {t("msg.speed", { n: u.speed >= 100 ? Math.round(u.speed) : u.speed.toFixed(1) })}
          </span>
        )}
      </span>
      {total > 0 && (
        <span className="rs-r">
          <span className="rs-tok" title={tokTitle} aria-label={tokTitle}>
            {tokfmt(total)} tokens
          </span>
          {u.cost !== undefined && (
            <span className="rs-cost" title={t("msg.costTitle")}>
              {costfmt(u.cost)}
            </span>
          )}
          {u.local && (
            <span className="rs-cost free" title={t("msg.localTitle")}>
              {t("msg.local")}
            </span>
          )}
        </span>
      )}
    </div>
  );
}

/** A user message: copy, and edit and resend in a one-to-one conversation. */
/** Text with "@Name" / "@id" of known agents shown as mention chips. */
function WithMentions({ text }: { text: string }) {
  const agents = useStore((s) => s.agents);
  const parts = useMemo(() => {
    if (!text.includes("@")) return null;
    const handles = new Set<string>();
    for (const a of Object.values(agents)) {
      handles.add(a.name);
      handles.add(a.id);
    }
    const list = [...handles].filter(Boolean).sort((a, b) => b.length - a.length);
    if (!list.length) return null;
    const esc = list.map((h) => h.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
    const re = new RegExp("(^|[\\s(（，,。])@(" + esc.join("|") + ")(?=$|[\\s,，。.!！?？:：)）])", "g");
    const out: (string | { h: string })[] = [];
    let last = 0;
    for (const m of text.matchAll(re)) {
      const at = m.index! + m[1].length;
      if (at > last) out.push(text.slice(last, at));
      out.push({ h: m[2] });
      last = at + 1 + m[2].length;
    }
    if (!out.length) return null;
    if (last < text.length) out.push(text.slice(last));
    return out;
  }, [text, agents]);
  if (!parts) return <>{text}</>;
  return (
    <>
      {parts.map((x, i) => (typeof x === "string" ? x : <span key={i} className="mchip">@{x.h}</span>))}
    </>
  );
}

/** Characters of a user message shown before it folds. */
const LONG_MSG = 400;

function UserMsg({ it }: { it: Extract<FeedItem, { k: "user" }> }) {
  const acts = useContext(MsgCtx);
  const long = (it.text?.length ?? 0) > LONG_MSG * 1.5;
  const [expanded, setExpanded] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(it.text);
  const [busy, setBusy] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const editable = !!acts && !it.superseded && it.seq > acts.compactedUpto && (!it.to || it.to === acts.agent.id) && !!it.text;
  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 320) + "px";
  }, [draft, editing]);
  useEffect(() => {
    if (editing) {
      const el = ta.current;
      el?.focus();
      el?.setSelectionRange(el.value.length, el.value.length);
    }
  }, [editing]);
  const submit = async () => {
    if (!acts || !draft.trim() || busy || !acts.idle) return;
    setBusy(true);
    const ok = await acts.resend(it.seq, draft);
    setBusy(false);
    if (ok) setEditing(false);
  };
  if (editing && acts)
    return (
      <div className="m-user">
        <div className="bubble editing">
          <textarea
            ref={ta}
            value={draft}
            aria-label={t("msg.edit")}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                setEditing(false);
              } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void submit();
              }
            }}
          />
          <div className="editfoot">
            <span className="small">{t("msg.editHint")}</span>
            <span className="editbtns">
              <button type="button" className="btn ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </button>
              <button
                type="button"
                className="btn pri"
                disabled={!draft.trim() || busy || !acts.idle}
                title={acts.idle ? t("msg.editKeys", { key: MAC ? "⌘↩" : "Ctrl+↩" }) : t("msg.busy", { name: acts.agent.name })}
                onClick={() => void submit()}
              >
                {t("msg.send")}
              </button>
            </span>
          </div>
        </div>
      </div>
    );
  return (
    <div className="m-user">
      {it.attachments?.length ? <MsgAttachments agent={it.to} items={it.attachments} /> : null}
      {(it.text || !it.attachments?.length) && (
        <div className={"bubble" + (long && !expanded ? " clipped" : "")}>
          {it.from && <span className="from">{it.from}</span>}
          <WithMentions text={long && !expanded ? it.text.slice(0, LONG_MSG) + "…" : it.text} />
          {long && (
            <button type="button" className="bubmore" onClick={() => setExpanded(!expanded)}>
              {expanded ? t("msg.collapse") : t("msg.expand", { n: it.text.length.toLocaleString() })}
            </button>
          )}
        </div>
      )}
      {it.text && <LinkCards text={it.text} mine />}
      {it.text && (
        <div className="msgacts">
          {editable && (
            <button
              type="button"
              className="ix mact"
              disabled={!acts!.idle}
              aria-label={t("msg.edit")}
              title={acts!.idle ? t("msg.editTitle") : t("msg.busy", { name: acts!.agent.name })}
              onClick={() => {
                setDraft(it.text);
                setEditing(true);
              }}
            >
              <Pencil size={14} />
            </button>
          )}
          <CopyBtn text={it.text} />
        </div>
      )}
    </div>
  );
}

/** Messages a regenerate / edit took back from the model: one line,
 *  opened on demand, shown dimmed. */
function Superseded({ rw, rows, agents }: { rw: Rewind; rows: FeedItem[]; agents: Record<string, AgentView> }) {
  const [open, setOpen] = useState(false);
  const n = rows.filter((x) => x.k === "user" || x.k === "agent" || x.k === "turn").length;
  return (
    <div className={"supd" + (open ? " open" : "")}>
      <button type="button" className="supline" aria-expanded={open} onClick={() => setOpen(!open)}>
        <History size={13} aria-hidden />
        <span>{t(rw.reason === "edit" ? "msg.supEdit" : "msg.supRegen")}</span>
        <span className="muted">· {t("msg.supHint", { n })}</span>
        <ChevronRight size={13} className="ch" aria-hidden />
      </button>
      {open && (
        <div className="supbody">
          <MsgCtx.Provider value={null}>
            {groupFeed(rows).map((g) =>
              g.items.map((x) => <FeedRow key={x.it.id} it={x.it} agents={agents} showModel={x.showModel} />),
            )}
          </MsgCtx.Provider>
        </div>
      )}
    </div>
  );
}

/** Rows of one feed run, grouped as the feed renders them. */
function Rows({ items, agents }: { items: FeedItem[]; agents: Record<string, AgentView> }) {
  return (
    <>
      {groupFeed(items).map((g) =>
        g.sys ? (
          <div className="sysgrp" key={g.items[0].it.id}>
            {g.items.map((x) => (
              <FeedRow key={x.it.id} it={x.it} agents={agents} />
            ))}
          </div>
        ) : (
          g.items.map((x) => <FeedRow key={x.it.id} it={x.it} agents={agents} showModel={x.showModel} />)
        ),
      )}
    </>
  );
}

/** The leading turn of a run: its user messages and what answered them,
 *  up to the next user message after a reply. */
function leadTurn(items: FeedItem[]): number {
  let replied = false;
  for (let i = 0; i < items.length; i++) {
    const k = items[i].k;
    if (k === "user" && replied) return i;
    if (k === "agent" || k === "turn") replied = true;
  }
  return items.length;
}

/** Answers to the same message after 重新生成: the one the model sees
 *  (last) and the earlier ones, switched with ‹ n/m ›. */
function Versions({ older, current, agents }: { older: FeedItem[][]; current: FeedItem[]; agents: Record<string, AgentView> }) {
  const all = [...older, current];
  const [i, setI] = useState(all.length - 1);
  const at = Math.min(i, all.length - 1);
  const live = at === all.length - 1;
  const shown = all[at];
  // the question once (from the shown version), then its answer
  const q = shown.filter((x) => x.k === "user" || x.k === "time");
  const a = shown.filter((x) => x.k !== "user" && x.k !== "time");
  return (
    <div className={"versions" + (live ? "" : " old")}>
      <Rows items={q} agents={agents} />
      {live ? (
        <Rows items={a} agents={agents} />
      ) : (
        <div className="supbody">
          <MsgCtx.Provider value={null}>
            <Rows items={a} agents={agents} />
          </MsgCtx.Provider>
        </div>
      )}
      <div className="vswitch" role="group" aria-label={t("msg.versions")}>
        <button type="button" className="ib" aria-label={t("msg.prevVersion")} disabled={at === 0} onClick={() => setI(at - 1)}>
          <ChevronLeft size={13} />
        </button>
        <span>{t("msg.versionOf", { n: at + 1, m: all.length })}</span>
        <button type="button" className="ib" aria-label={t("msg.nextVersion")} disabled={live} onClick={() => setI(at + 1)}>
          <ChevronRight size={13} />
        </button>
        {!live && <span className="muted">· {t("msg.oldVersion")}</span>}
      </div>
    </div>
  );
}

/** Messages and turns of a conversation rendered at first (and added per
 *  显示更早的). */
const FEED_WINDOW = 80;

/** Segments as rendered: runs of regenerated answers followed by the one
 *  the model sees become versions; other runs stay as they are. */
type Block = { kind: "rows"; items: FeedItem[] } | { kind: "sup"; rw: Rewind; items: FeedItem[] } | { kind: "versions"; older: FeedItem[][]; current: FeedItem[] };
function blocks(items: FeedItem[]): Block[] {
  const segs = segments(items);
  const out: Block[] = [];
  for (let i = 0; i < segs.length; i++) {
    const s = segs[i];
    if (s.sup?.reason === "regenerate") {
      const older: FeedItem[][] = [];
      let j = i;
      while (j < segs.length && segs[j].sup?.reason === "regenerate") older.push(segs[j++].items);
      const next = segs[j];
      if (next && !next.sup && older.every((o) => o.some((x) => x.k === "agent" || x.k === "turn"))) {
        const n = leadTurn(next.items);
        out.push({ kind: "versions", older, current: next.items.slice(0, n) });
        if (n < next.items.length) out.push({ kind: "rows", items: next.items.slice(n) });
        i = j;
        continue;
      }
    }
    out.push(s.sup ? { kind: "sup", rw: s.sup, items: s.items } : { kind: "rows", items: s.items });
  }
  return out;
}

/** The feed split into runs the model still sees and runs it does not. */
function segments(items: FeedItem[]): { sup?: Rewind; items: FeedItem[] }[] {
  const out: { sup?: Rewind; items: FeedItem[] }[] = [];
  for (const it of items) {
    const last = out[out.length - 1];
    if (last && last.sup?.at === it.superseded?.at) last.items.push(it);
    else out.push({ sup: it.superseded, items: [it] });
  }
  return out;
}

// ---------------------------------------------------------------- feed

function Feed({
  sessionId,
  agent,
  channel,
  items,
  working,
  loading,
  error,
}: {
  sessionId: string;
  agent: AgentView;
  channel?: ChannelView;
  items: FeedItem[];
  /** agents whose turn here is open (turn/start without turn/end) */
  working?: Set<string>;
  loading: boolean;
  error?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const streams = useStore((s) => s.streams);
  const agents = useStore((s) => s.agents);
  const bubbles = useStore((s) => s.bubbles);
  const live = Object.entries(streams).filter(([k]) => k.startsWith(sessionId + "|"));
  // agents whose turn is open in the feed: their live output grows inside it
  const openTurns = useMemo(() => new Set(items.filter((i): i is Extract<FeedItem, { k: "turn" }> => i.k === "turn" && i.open).map((i) => i.agent)), [items]);
  const approvals = useStore((s) => s.approvals);
  // who is working with nothing on screen yet: in a chat the agent; in a
  // channel every member whose turn here is open (each says so on its own line)
  const typingIds = (channel ? channel.members.filter((m) => working?.has(m) || (m === agent.id && agent.status === "running")) : agent.status === "running" ? [agent.id] : [])
    .filter((id) => agents[id]?.status === "running" || id === agent.id)
    .filter((id) => !approvals.some((a) => a.agent === id && a.session === sessionId))
    .filter((id) => !live.some(([k, v]) => k.endsWith("|" + id) && v.text))
    .filter((id) => !openTurns.has(id));
  // Agents in this conversation that wait for a person to take over their browser.
  const takeovers = useMemo(() => {
    const ids = channel ? channel.members : [agent.id];
    const pendingCall = new Set<string>();
    for (const it of items)
      if (it.k === "turn")
        for (const p of it.parts) if (p.k === "steps") for (const st of p.steps) if (st.call.name === "browser_request_takeover" && !st.result) pendingCall.add(it.agent);
    return ids
      .map((id) => agents[id])
      .filter(
        (a): a is AgentView =>
          !!a && !!takeoverReason(a) && (a.statusSession ? a.statusSession === sessionId : pendingCall.has(a.id) || (!channel && a.session === sessionId)),
      );
  }, [items, agents, channel, agent.id, sessionId]);

  // A long conversation renders its latest blocks only; 显示更早的 adds more
  // above, keeping what is on screen where it was.
  const [shownN, setShownN] = useState(FEED_WINDOW);
  useEffect(() => setShownN(FEED_WINDOW), [sessionId]);
  // cut before the last shownN messages / turns, at a message boundary
  const { cut, hidden } = useMemo(() => {
    let n = 0;
    for (let i = items.length - 1; i >= 0; i--) {
      const k = items[i].k;
      if (k === "user" || k === "turn" || k === "agent") n++;
      if (n > shownN && k === "user") return { cut: i + 1, hidden: items.slice(0, i + 1).filter((x) => x.k === "user" || x.k === "turn" || x.k === "agent").length };
    }
    return { cut: 0, hidden: 0 };
  }, [items, shownN]);
  const all = useMemo(() => blocks(cut ? items.slice(cut) : items), [items, cut]);
  const anchor = useRef<number | null>(null);
  const showOlder = () => {
    const el = ref.current;
    anchor.current = el ? el.scrollHeight - el.scrollTop : null;
    setShownN((n) => n + FEED_WINDOW);
  };
  const onScroll = () => {
    const el = ref.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (anchor.current !== null) {
      el.scrollTop = el.scrollHeight - anchor.current;
      anchor.current = null;
      return;
    }
    if (stick.current) el.scrollTop = el.scrollHeight;
  });

  // your bubbles can take the agent's colour (a channel's own colour)
  const bubbleColor = channel ? channelColor(channel.id) : agent.kind === "chat" ? channelColor(agent.id) : agent.color || colorFor(agent.id);
  return (
    <div
      className="feed"
      ref={ref}
      onScroll={onScroll}
      data-bubbles={bubbles}
      style={bubbles === "neutral" ? undefined : ({ "--ac": bubbleColor, "--ac-ink": ink(bubbleColor) } as CSSProperties)}
    >
      {error && (
        <div className="sys err">
          <span className="dot err" />
          {t("chat.loadFailed", { error })}
          <button className="linkbtn" type="button" onClick={() => loadSession(sessionId)}>
            {t("common.retry")}
          </button>
        </div>
      )}
      {!loading && items.length === 0 && !channel && (
        <div className="feedintro">
          <Avatar agent={agent} size={56} />
          <b>{agent.kind === "chat" ? t("chat.plainEmpty") : t("chat.empty", { name: agent.name })}</b>
          {/* the role says what it is for; a folder only when the person chose one */}
          {agent.role && agent.kind !== "chat" && <span className="small">{agent.role}</span>}
          <span className="small">{agent.kind === "chat" ? t("chat.plainEmptyDesc") : agent.sharedWorkspace ? t("chat.emptyInDir", { dir: shortPath(agent.sharedWorkspace) }) : t("chat.emptyDesc")}</span>
        </div>
      )}
      {!loading && channel && !items.some((i) => i.k === "user" || i.k === "agent" || i.k === "turn") && (
        <div className="feedintro">
          <div className="avstack">
            {channel.members.map((id) => (
              <Avatar key={id} agent={agents[id]} size={44} />
            ))}
          </div>
          <b>{t("ch.empty", { name: channel.title || channel.id })}</b>
          <span className="small">{t("ch.emptyDesc", { owner: agents[channel.owner]?.name ?? channel.owner })}</span>
        </div>
      )}
      <SessionCtx.Provider value={sessionId}>
      {hidden > 0 && (
        <button className="feedolder" type="button" onClick={showOlder}>
          {t("chat.showOlder", { n: hidden })}
        </button>
      )}
      {all.map((b) =>
        b.kind === "sup" ? (
          <Superseded key={"sup" + b.items[0].id} rw={b.rw} rows={b.items} agents={agents} />
        ) : b.kind === "versions" ? (
          <Versions key={"v" + b.older[0][0].id} older={b.older} current={b.current} agents={agents} />
        ) : (
          <Rows key={"r" + b.items[0].id} items={b.items} agents={agents} />
        ),
      )}
      </SessionCtx.Provider>
      {live.map(([k, v]) => {
        const a = agents[k.split("|")[1]];
        if (!v.text && !v.thinking) return null;
        if (openTurns.has(k.split("|")[1])) return null; // shown inside its turn
        return (
          <div className="m-agent live" key={k}>
            <Avatar agent={a} size={28} />
            <div style={{ minWidth: 0 }}>
              <div className="mh">
                <b>{a?.name ?? k.split("|")[1]}</b>
                <span className="t">{t("status.running")}</span>
              </div>
              {v.thinking && !v.text && <div className="thinking small">{v.thinking.slice(-400)}</div>}
              {v.text && (
                <div className="mt">
                  <Markdown text={v.text} />
                  <span className="caret" />
                </div>
              )}
            </div>
          </div>
        );
      })}
      {takeovers.map((a) => (
        <TakeoverCard key={a.id} agent={a} reason={takeoverReason(a) ?? ""} />
      ))}
      {typingIds.map((id) => {
        const a = id === agent.id ? agent : agents[id];
        if (!a) return null;
        return (
          <div className="typing" key={"ty" + id}>
            <Avatar agent={a} size={24} />
            <span>{detailLabel(a.detail) || t("chat.thinking", { name: speaker(a) })}</span>
            <span className="dots">
              <i />
              <i />
              <i />
            </span>
          </div>
        );
      })}
    </div>
  );
}

const SYS_KINDS = new Set<FeedItem["k"]>(["switch", "mode", "compact", "handover", "owner", "chstate", "sys"]);

function isSysItem(it: FeedItem): boolean {
  return SYS_KINDS.has(it.k) || (it.k === "approval" && !!it.decided);
}

/** Split the feed into runs; consecutive system lines become one compact group.
 *  Agent messages show their model only when it differs from the previous one. */
type Row = { it: FeedItem; showModel?: boolean };

function groupFeed(items: FeedItem[]): { sys: boolean; items: Row[] }[] {
  const out: { sys: boolean; items: Row[] }[] = [];
  let prev = "";
  for (const it of items) {
    const sys = isSysItem(it);
    let showModel: boolean | undefined;
    const m = it.k === "turn" ? lastMsg(it)?.model : it.k === "agent" ? it.model : undefined;
    if (m) {
      const key = m.provider + "/" + m.model + "@" + (m.effort ?? "");
      showModel = key !== prev;
      prev = key;
    }
    const last = out[out.length - 1];
    if (last && sys && last.sys) last.items.push({ it });
    else out.push({ sys, items: [{ it, showModel }] });
  }
  return out;
}

const lastMsg = (t: Extract<FeedItem, { k: "turn" }>): AgentItem | undefined => {
  for (let i = t.parts.length - 1; i >= 0; i--) {
    const p = t.parts[i];
    if (p.k === "msg") return p.it;
  }
  return undefined;
};

function PluginFeedCard({ type, data }: { type: string; data: any }) {
  const c = useSlot("conversation.node").find((x) => x.key === type);
  return c ? <PluginCard c={c} data={data} /> : null;
}

function FeedRow({ it, agents, showModel }: { it: FeedItem; agents: Record<string, AgentView>; showModel?: boolean }) {
  const models = useStore((s) => s.models);
  switch (it.k) {
    case "time":
      return <div className="tmark">{dayLabel(it.time)}</div>;
    case "user":
      return <UserMsg it={it} />;
    case "agent":
      return <AgentMsg it={it} agent={agents[it.agent]} showModel={!!showModel} />;
    case "turn":
      return <TurnMsg it={it} agent={agents[it.agent]} showModel={!!showModel} />;
    case "steps":
      return <WorkingTurn agent={agents[it.agent]} id={it.agent} steps={it.steps} />;
    case "bg":
      return <BgCard it={it} agents={agents} />;
    case "approval":
      return <ApprovalCard it={it} agent={agents[it.req.agent]} />;
    case "switch": {
      const a = agents[it.agent];
      const reason = t((`chat.reason.${it.reason}` as Key) in REASONS ? (`chat.reason.${it.reason}` as Key) : "chat.reason.other");
      return (
        <div className="sys" title={it.detail}>
          <span className="dot err" />
          <span>
            {t("chat.modelSwitched", {
              agent: a?.name ?? it.agent,
              from: it.from ? modelLabel(models, it.from) : "?",
              to: it.to ? modelLabel(models, it.to) : "?",
              reason,
            })}
          </span>
        </div>
      );
    }
    case "mode": {
      const a = agents[it.agent];
      return (
        <div className="sys">
          {it.to === "host" ? <Monitor size={14} /> : <Shield size={14} />}
          <span>
            {t("chat.modeChanged", { agent: a?.name ?? it.agent, mode: modeLabel(it.to) })}
            {it.until ? t("chat.modeUntil", { time: until(it.until) }) : ""}
          </span>
        </div>
      );
    }
    case "compact":
      return <Compact it={it} />;
    case "handover": {
      const from = agents[it.from];
      const to = agents[it.to];
      return <Handover it={it} from={from} to={to} />;
    }
    case "owner": {
      const o = agents[it.owner]?.name ?? it.owner;
      const by = !it.by || it.by === "desktop" || it.by === "mobile" ? t("common.you") : agents[it.by]?.name ?? it.by;
      return (
        <div className="sys">
          <UserRound size={14} />
          <span>{it.reason === "reassigned" ? t("ch.reassigned", { by, name: o }) : t("ch.assigned", { by, name: o })}</span>
        </div>
      );
    }
    case "chstate":
      return (
        <div className="sys muted">
          {it.state === "archived" ? <Archive size={14} /> : it.state === "paused" ? <Pause size={14} /> : <Play size={14} />}
          <span>{t(it.state === "archived" ? "ch.stArchived" : it.state === "paused" ? "ch.stPaused" : "ch.stActive")}</span>
        </div>
      );
    case "plugin":
      return <PluginFeedCard type={it.type} data={it.data} />;
    case "media":
      return <MediaCard it={it} />;
    case "sys":
      return (
        <div className={"sys " + (it.tone === "error" ? "err" : it.tone)}>
          {it.tone === "error" && <span className="dot err" />}
          {it.tone === "error" && MODEL_UNAVAILABLE.test(it.text) ? (
            <span title={it.text}>{t("chat.modelUnavailableErr")}</span>
          ) : it.tone === "error" && NO_MODEL.test(it.text) ? (
            <>
              <span>{t("chat.noModelErr")}</span>
              <button className="linkbtn" type="button" onClick={openConnect}>
                {t("conn.connect")}
              </button>
            </>
          ) : it.key === "chat.modelChosen" && it.vars ? (
            <span>{t("chat.modelChosen", { agent: agents[it.vars.agent]?.name ?? it.vars.agent, to: (() => { const r = parseRef(it.vars.to ?? ""); return r ? shortModelEffort(models, r).name : it.vars.to ?? ""; })() })}</span>
          ) : (
            <span>{it.key ? t(it.key as Key, it.vars) : it.text}</span>
          )}
          {it.tone === "error" && <RetryButton id={it.id} />}
        </div>
      );
  }
}

/** 重试 on the error row of the last turn when it failed without a reply:
 *  resends that turn's messages as they were (like regenerate). */
function RetryButton({ id }: { id: string }) {
  const acts = useContext(MsgCtx);
  const [busy, setBusy] = useState(false);
  if (!acts || acts.lastTurn?.item !== id || acts.lastTurn.from <= acts.compactedUpto) return null;
  return (
    <button
      className="linkbtn"
      type="button"
      disabled={!acts.idle || busy}
      title={acts.idle ? t("msg.retryTitle") : t("msg.busy", { name: acts.agent.name })}
      onClick={async () => {
        setBusy(true);
        await acts.resend(acts.lastTurn!.from);
        setBusy(false);
      }}
    >
      {t("msg.retry")}
    </button>
  );
}

const REASONS: Record<string, true> = {
  "chat.reason.quota_exhausted": true,
  "chat.reason.rate_limited": true,
  "chat.reason.unavailable": true,
  "chat.reason.model_unavailable": true,
  "chat.reason.model_not_found": true,
};

function AgentMsg({ it, agent, showModel, bare }: { it: AgentItem; agent?: AgentView; showModel: boolean; bare?: boolean }) {
  const models = useStore((s) => s.models);
  const [showThink, setShowThink] = useState(false);
  const [reading, setReading] = useState(false);
  const acts = useContext(MsgCtx);
  const [regen, setRegen] = useState(false);
  const sm = it.model ? shortModelEffort(models, it.model) : null;
  // regenerate: the last finished turn of a one-to-one conversation
  const canRegen = !!acts && !it.superseded && acts.lastTurn?.item === it.id && acts.lastTurn.from > acts.compactedUpto;
  const hasActs = !!it.text || canRegen;
  return (
    <Wrap bare={bare} final={!!it.stats} agent={agent}>
        {!bare && <div className="mh">
          <b>{agent ? speaker(agent) : it.agent}</b>
          <span className="t">{hhmm(it.time)}</span>
          {/* the turn's last reply names its model in the footer */}
          {sm && it.model && !it.stats && (
            <span className={"via" + (showModel ? "" : " hov")} title={modelLabel(models, it.model)}>
              {sm.name}
              {sm.effort && <span className="effs">{sm.effort}</span>}
            </span>
          )}
        </div>}
        {!bare && it.thinking && (
          <button className="row-btn think" type="button" aria-expanded={showThink} onClick={() => setShowThink(!showThink)}>
            {t("chat.thoughts")}
            <ChevronRight size={14} className="ch" />
          </button>
        )}
        {showThink && <div className="thinking small">{it.thinking}</div>}
        {it.searches.map((q, i) => (
          <div className="srvtool" key={i}>
            <SearchIcon size={13} />
            <span>{t("chat.searched", { q })}</span>
          </div>
        ))}
        {it.text && (
          <div className="mt">
            <Markdown text={it.text} />
          </div>
        )}
        {it.text && <LocalPreviewCards text={it.text} />}
        {it.refusal && (
          <div className="refusal">
            <Info size={14} />
            <span>{t("chat.refusal", { name: agent?.name ?? it.agent })}</span>
          </div>
        )}
        {it.citations.length > 0 && (
          <div className="cites">
            <span className="small">{t("chat.sources")}</span>
            {it.citations.map((c, i) => (
              <a key={c.url} className="cite" href={c.url} target="_blank" rel="noreferrer noopener" title={c.url}>
                <span className="n">{i + 1}</span>
                <span className="site">{siteOf(c.url)}</span>
                <span className="ct">{c.title}</span>
              </a>
            ))}
          </div>
        )}
        {/* the answer, once the turn is over (never narration or a stream) */}
        {it.stats && it.text && <LinkCards text={it.text} exclude={it.asked} />}
        {it.stats && <ReplyStats stats={it.stats} />}
        {hasActs && (
          <div className="msgacts">
            {it.text && <CopyBtn text={it.text} />}
            {canRegen && (
              <button
                type="button"
                className="ix mact"
                disabled={!acts!.idle || regen}
                aria-label={t("msg.regenerate")}
                title={acts!.idle ? t("msg.regenerateTitle") : t("msg.busy", { name: acts!.agent.name })}
                onClick={async () => {
                  setRegen(true);
                  await acts!.resend(acts!.lastTurn!.from);
                  setRegen(false);
                }}
              >
                <RefreshCw size={14} />
              </button>
            )}
            {it.text && speakSupported() && (
              <button
                type="button"
                className={"ix mact" + (reading ? " on" : "")}
                aria-label={reading ? t("voice.stop") : t("voice.read")}
                title={reading ? t("voice.stop") : t("voice.read")}
                onClick={() => {
                  setReading(!speaking(it.id));
                  speak(it.id, it.text, () => setReading(false));
                }}
              >
                {reading ? <Square size={12} /> : <Volume2 size={14} />}
              </button>
            )}
          </div>
        )}
    </Wrap>
  );
}

/** A message's frame: avatar and column, or nothing inside a turn. */
function Wrap({ bare, final, agent, children }: { bare?: boolean; final?: boolean; agent?: AgentView; children: ReactNode }) {
  if (bare) return <div className="tans">{children}</div>;
  return (
    <div className={"m-agent" + (final ? " final" : "")}>
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>{children}</div>
    </div>
  );
}

/** One agent's turn: one header, then its thinking (one toggle), what it
 *  said while working (narration, small), its steps and its answer, in
 *  order; while it runs, the text being written grows at the end. */
function TurnMsg({ it, agent }: { it: Extract<FeedItem, { k: "turn" }>; agent?: AgentView; showModel?: boolean }) {
  const models = useStore((s) => s.models);
  const sid = useContext(SessionCtx);
  const live = useStore((s) => (it.open ? s.streams[sid + "|" + it.agent] : undefined));
  const waiting = useStore((s) => s.approvals.some((a) => a.agent === it.agent && a.session === sid));
  // between steps, with nothing being written yet: say it is working
  const lastPart = it.parts[it.parts.length - 1];
  const stepRunning = lastPart?.k === "steps" && lastPart.steps.some((x) => !x.result);
  const working = it.open && agent?.status === "running" && !waiting && !stepRunning && !live?.text;
  const [showThink, setShowThink] = useState(false);
  const msgs = it.parts.filter((p): p is Extract<TurnPart, { k: "msg" }> => p.k === "msg");
  const last = lastMsg(it);
  const thoughts = msgs.map((p) => p.it.thinking).filter(Boolean);
  const answer = msgs.find((p) => !p.narration && p.it.stats)?.it;
  const sm = last?.model ? shortModelEffort(models, last.model) : null;
  return (
    <div className={"m-agent turn" + (answer ? " final" : "")}>
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>
        <div className="mh">
          <b>{agent ? speaker(agent) : it.agent}</b>
          <span className="t">{hhmm(it.time)}</span>
          {/* while it works, which model; a finished turn names it in its footer */}
          {sm && last?.model && !answer && (
            <span className="via" title={modelLabel(models, last.model)}>
              {sm.name}
              {sm.effort && <span className="effs">{sm.effort}</span>}
            </span>
          )}
        </div>
        {thoughts.length > 0 && (
          <>
            <button className="row-btn think" type="button" aria-expanded={showThink} onClick={() => setShowThink(!showThink)}>
              {t("chat.thoughts")}
              {thoughts.length > 1 && <span className="muted"> · {thoughts.length}</span>}
              <ChevronRight size={14} className="ch" />
            </button>
            {showThink && (
              <div className="thinking small">
                {thoughts.map((x, i) => (
                  <p key={i}>{x}</p>
                ))}
              </div>
            )}
          </>
        )}
        {it.parts.map((p) =>
          p.k === "steps" ? (
            <Steps key={p.id} steps={p.steps} agent={it.agent} />
          ) : p.k === "preview" ? (
            <PreviewLine key={p.id} p={p.p} session={sid} />
          ) : p.narration ? (
            p.it.text || p.it.searches.length ? (
              <div className={"narr" + (it.open ? " fresh" : "")} key={p.it.id}>
                {p.it.searches.map((q, i) => (
                  <div className="srvtool" key={i}>
                    <SearchIcon size={13} />
                    <span>{t("chat.searched", { q })}</span>
                  </div>
                ))}
                {p.it.text && (
                  <div className="mt">
                    <Markdown text={p.it.text} />
                  </div>
                )}
              </div>
            ) : null
          ) : (
            <AgentMsg key={p.it.id} it={p.it} agent={agent} showModel={false} bare />
          ),
        )}
        {working && !live?.thinking && (
          <div className="typing in">
            <span>{detailLabel(agent!.detail) || t("chat.thinking", { name: speaker(agent!) })}</span>
            <span className="dots">
              <i />
              <i />
              <i />
            </span>
          </div>
        )}
        {live && (live.text || live.thinking) && (
          <div className="tans">
            {live.thinking && !live.text && <div className="thinking small">{live.thinking.slice(-400)}</div>}
            {live.text && (
              <div className="mt">
                <Markdown text={live.text} />
                <span className="caret" />
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
/** A sub-agent's session, read-only (the right panel shows it). The
 *  sub-agent itself is usually gone once its task is done: it is named
 *  after its parent. */
export function SubFeed({ sessionId, child, parent }: { sessionId: string; child: string; parent?: AgentView }) {
  const events = useStore((s) => s.events[sessionId]);
  const err = useStore((s) => s.loadErr[sessionId]);
  const live = useStore((s) => s.agents);
  useEffect(() => {
    void loadSession(sessionId);
  }, [sessionId]);
  const agents = useMemo(() => {
    if (live[child]) return live;
    const stand = { ...(parent ?? {}), id: child, name: t("sub.name", { parent: parent?.name ?? "" }) } as AgentView;
    return { ...live, [child]: stand };
  }, [live, child, parent]);
  // the task arrives as a message from the parent: name it, not its id
  const items = useMemo(
    () => project(events ?? []).items.map((it) => (it.k === "user" && parent && it.from === parent.id ? { ...it, from: parent.name } : it)),
    [events, parent],
  );
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && el.scrollHeight - el.scrollTop - el.clientHeight < 120) el.scrollTop = el.scrollHeight;
  });
  if (err) return <p className="small errt">{err}</p>;
  if (!events) return <p className="small muted">{t("sub.loading")}</p>;
  return (
    <div className="subfeed" ref={ref}>
      {groupFeed(items).map((g) =>
        g.items.map((x) => <FeedRow key={x.it.id} it={x.it} agents={agents} />),
      )}
    </div>
  );
}

/** Steps an agent is taking with no message yet: under its own header. */
function WorkingTurn({ agent, id, steps }: { agent?: AgentView; id: string; steps: Step[] }) {
  return (
    <div className="m-agent">
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>
        <div className="mh">
          <b>{agent ? speaker(agent) : id}</b>
          {steps[0]?.time && <span className="t">{hhmm(steps[0].time)}</span>}
        </div>
        <Steps steps={steps} agent={agent?.id ?? id} />
      </div>
    </div>
  );
}

/** A result that came back on its own: one line, opened on demand. */
function BgCard({ it, agents }: { it: Extract<FeedItem, { k: "bg" }>; agents: Record<string, AgentView> }) {
  const [open, setOpen] = useState(false);
  const who = it.who ? (Object.values(agents).find((a) => a.id === it.who || a.name === it.who)?.name ?? it.who) : "";
  const label =
    it.kind === "delegation"
      ? t(it.ok ? "bg.delegationDone" : "bg.delegationFailed", { who: who || t("bg.subAgent") })
      : it.kind === "followup"
        ? t("bg.followup")
        : t(it.ok ? "bg.jobDone" : "bg.jobFailed");
  const what = it.kind === "delegation" ? it.task || "" : it.title;
  const Icon = it.kind === "delegation" ? Users : it.kind === "followup" ? AlarmClock : Terminal;
  const canOpen = !!it.body;
  return (
    <div className={"bgcard" + (it.ok ? "" : " bad") + (open ? " open" : "")}>
      <button className="bghead" type="button" aria-expanded={canOpen ? open : undefined} disabled={!canOpen} onClick={() => setOpen(!open)}>
        <span className="bgic">
          <Icon size={13} />
        </span>
        <span className="bglabel">{label}</span>
        {what && <span className="bgwhat">{what}</span>}
        <span className="t">{hhmm(it.time)}</span>
        {canOpen && <ChevronRight size={14} className="ch" />}
      </button>
      {it.sub && (
        <button className="bgsub linkbtn" type="button" onClick={() => openSubRun(it.sub!)}>
          {t("sub.viewRun")}
        </button>
      )}
      {open && canOpen && (
        <div className="bgbody">
          <Markdown text={it.body} />
        </div>
      )}
    </div>
  );
}

function siteOf(u: string): string {
  try {
    return new URL(u).hostname.replace(/^www\./, "");
  } catch {
    return u;
  }
}

function Handover({ it, from, to }: { it: Extract<FeedItem, { k: "handover" }>; from?: AgentView; to?: AgentView }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="handover">
      <div className="sys">
        <ArrowRightLeft size={14} />
        <Avatar agent={from} size={18} />
        <span>
          {t("ch.handover", { from: from?.name ?? it.from, to: to?.name ?? it.to })}
        </span>
        <Avatar agent={to} size={18} />
        {(it.note || it.files.length > 0) && (
          <button className="linkbtn" type="button" onClick={() => setOpen(!open)}>
            {open ? t("ch.hideNote") : t("ch.showNote")}
          </button>
        )}
      </div>
      {open && (
        <div className="brief">
          {it.note && <Markdown text={it.note} />}
          {it.files.length > 0 && (
            <div className="hfiles">
              {it.files.map((f) => (
                <span key={f} className="fchip mono">
                  <Link2 size={12} />
                  {f}
                </span>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** How a finished step went: a declined approval or a command's non-zero
 *  exit (grep finding nothing, cat of a missing file) is not a failure of
 *  the tool, and is not shown as one. */
type Outcome = "ok" | "declined" | "exit" | "error";
function outcome(s: Step): Outcome {
  const r = s.result;
  if (!r || !r.isError) return "ok";
  if (/^not approved \(/.test(r.content)) return "declined";
  if (/^Exit code: [1-9]\d*\n/.test(r.content)) return "exit";
  return "error";
}

function Steps({ steps, agent }: { steps: Step[]; agent?: string }) {
  const [open, setOpen] = useState(false);
  const running = steps.some((s) => !s.result && !s.awaiting);
  const awaiting = steps.some((s) => s.awaiting);
  const errors = steps.filter((s) => outcome(s) === "error").length;
  const declined = steps.filter((s) => outcome(s) === "declined").length;
  // one step needs no "ran 1 step" wrapper: the step itself is the line
  if (steps.length === 1)
    return (
      <div className="stepsw one">
        <StepRow s={steps[0]} agent={agent} />
      </div>
    );
  return (
    <div className="stepsw">
      <button className="row-btn" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {running ? <span className="spin" /> : <Wrench size={14} className="muted" />}
        {running ? t("chat.stepsRunning", { n: steps.length }) : t("chat.steps", { n: steps.length })}
        {awaiting && <span className="pill amber">{t("appr.waiting")}</span>}
        {declined > 0 && <span className="pill">{t("chat.toolDeclined")}</span>}
        {errors > 0 && <span className="pill red">{t("chat.toolError")} {errors}</span>}
        <ChevronRight size={14} className="ch" />
      </button>
      {open && (
        <div className="stepsx">
          {steps.map((s) => (
            <StepRow key={s.call.id} s={s} agent={agent} />
          ))}
        </div>
      )}
    </div>
  );
}

function StepRow({ s, agent }: { s: Step; agent?: string }) {
  const [open, setOpen] = useState(false);
  const cu = computerLabel(s);
  const pv = cu ? "" : argsPreview(s.call.args);
  const shots = (s.computer ?? []).filter((c) => c.screenshot).map((c) => c.screenshot!);
  return (
    <div className="step">
      <button className="stephead" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {s.awaiting ? (
          <Hand size={13} className="waitc" />
        ) : !s.result ? (
          <span className="spin sm" />
        ) : outcome(s) === "error" ? (
          <CircleX size={13} className="errc" />
        ) : outcome(s) === "ok" ? (
          <Check size={13} className="okc" />
        ) : (
          <CircleMinus size={13} className="muted" />
        )}
        {cu ? <b className="cul">{cu}</b> : toolLabel(s.call.name) ? <b>{toolLabel(s.call.name)}</b> : <b className="mono">{s.call.name}</b>}
        {cu && (s.computer ?? []).some((c) => c.foreground) && <span className="pill sm">{t("tool.cu.fg")}</span>}
        <span className={"pv" + (PROSE_ARGS.has(s.call.name) ? "" : " mono")}>{pv}</span>
        {s.decision && <DecisionTag d={s.decision} />}
        {s.sub && (
          <span
            className="steplink"
            role="link"
            tabIndex={0}
            onClick={(e) => {
              e.stopPropagation();
              openSubRun(s.sub!);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.stopPropagation();
                openSubRun(s.sub!);
              }
            }}
          >
            {t("sub.view")}
          </span>
        )}
      </button>
      {shots.length > 0 && agent && (
        <div className="cushots">
          {shots.map((p) => (
            <CuShot key={p} agent={agent} path={p} />
          ))}
        </div>
      )}
      {open && (
        <div className="stepbody">
          {s.call.name === "computer_act_batch" && (s.computer?.length ?? 0) > 0 && (
            <ol className="custeps small">
              {s.computer!.map((c, i) => (
                <li key={i} className={c.ok ? "" : "errc"}>
                  {cuActionLabel(c.action, { target: c.elementName || (c.element ? `[${c.element}]` : ""), text: c.text ?? "", keys: c.keys ?? "", path: (c.menu ?? []).join(" › "), app: c.appName || c.app })}
                  {c.error ? " · " + c.error : ""}
                </li>
              ))}
            </ol>
          )}
          {s.decision?.message && (
            <p className="small stepnote">
              {t("appr.noteFrom", { by: decidedBy(s.decision) })}「{s.decision.message}」
            </p>
          )}
          <pre className="codeblk">{argsFull(s.call.args)}</pre>
          {s.result &&
            (s.result.content.startsWith(PARKED) ? (
              <p className="small muted">{t("appr.parked")}</p>
            ) : (
              <pre className={"codeblk" + (outcome(s) === "error" ? " errblk" : "")}>{clip(s.result.content)}</pre>
            ))}
        </div>
      )}
    </div>
  );
}

const decidedBy = (d: Decision) => (d.by && d.by !== "desktop" ? d.by : t("common.you"));

/** How an approval was answered, as a small tag on its tool step. */
function DecisionTag({ d }: { d: Decision }) {
  const ok = d.outcome === "allowed-once";
  const label = ok
    ? t(d.remember ? "appr.tagAlways" : "appr.tagApproved")
    : t(d.outcome === "rejected" ? "appr.tagRejected" : d.outcome === "cancelled" ? "appr.tagCancelled" : "appr.tagUnavailable");
  const who = decidedBy(d);
  return (
    <span className={"dtag" + (ok ? " ok" : " no")} title={t(ok ? "appr.tagByOk" : "appr.tagByNo", { by: who }) + (d.message ? ` · 「${d.message}」` : "")}>
      {ok ? <ShieldCheck size={11} /> : <ShieldX size={11} />}
      {label}
    </span>
  );
}

/** A saved screenshot of a computer_* step: a thumbnail that opens full size. */
function CuShot({ agent, path }: { agent: string; path: string }) {
  const src = useRawURL(agent, path);
  const name = path.split("/").pop() ?? path;
  return (
    <a href={src} target="_blank" rel="noreferrer" title={t("att.open", { name })}>
      {src ? <img src={src} alt={name} loading="lazy" /> : <span className="thumbph" />}
    </a>
  );
}

function parsedArgs(args: any): Record<string, any> {
  if (typeof args === "string") {
    try {
      return JSON.parse(args) ?? {};
    } catch {
      return {};
    }
  }
  return args && typeof args === "object" ? args : {};
}

type CuVars = { target: string; text: string; keys: string; path: string; app: string };

/** One computer action in words: "点击 新建备忘录", "按 cmd+n". */
function cuActionLabel(action: string, v: CuVars): string {
  switch (action) {
    case "press":
    case "click":
    case "double_click":
    case "right_click":
      return t(v.target ? "tool.cu.click" : "tool.cu.clickApp", v);
    case "set_value":
      return t("tool.cu.set", v);
    case "focus":
      return t("tool.cu.focus", v);
    case "type":
      return t("tool.cu.type", { ...v, text: v.text.length > 40 ? v.text.slice(0, 40) + "…" : v.text });
    case "key":
      return t("tool.cu.key", v);
    case "menu":
      return t("tool.cu.menu", v);
    case "scroll":
      return t("tool.cu.scroll", v);
    case "drag":
      return t("tool.cu.drag", v);
    case "observe":
      return t("tool.cu.observe", v);
    case "open":
      return t("tool.cu.open", v);
  }
  return t("tool.cu.act", v);
}

/** computer_* steps in words ("查看 备忘录", "点击 新建备忘录"); null for other tools. */
function computerLabel(s: Step): string | null {
  const n = s.call.name;
  if (!n.startsWith("computer_")) return null;
  const a = parsedArgs(s.call.args);
  const logs = s.computer ?? [];
  const app = String(logs[0]?.appName || a.app || "");
  switch (n) {
    case "computer_apps":
      return t("tool.cu.apps");
    case "computer_observe":
      return t("tool.cu.observe", { app });
    case "computer_open":
      return t("tool.cu.open", { app });
    case "computer_act_batch":
      return t("tool.cu.batch", { app, n: Array.isArray(a.actions) ? a.actions.length : logs.length });
    case "computer_script":
      return app ? t("tool.cu.scriptApp", { app }) : t("tool.cu.script");
    case "computer_shortcut":
      return t("tool.cu.shortcut", { name: String(a.name ?? "") });
    case "computer_shortcuts":
      return t("tool.cu.shortcuts");
  }
  const l = logs[logs.length - 1];
  const at = a.x != null && a.y != null ? `(${Math.round(a.x)}, ${Math.round(a.y)})` : "";
  return cuActionLabel(String(a.action ?? ""), {
    target: l?.elementName || (a.element ? `[${a.element}]` : at),
    text: String(a.text ?? ""),
    keys: String(a.keys ?? ""),
    path: Array.isArray(a.menu) ? a.menu.join(" › ") : "",
    app,
  });
}

/** Readable names of common tools (others show their tool name). */
const TOOL_LABELS: Record<string, Key> = {
  delegate: "tool.delegate",
  bash: "tool.bash",
  read_file: "tool.read",
  write_file: "tool.write",
  edit_file: "tool.edit",
  list_dir: "tool.list",
  web_fetch: "tool.fetch",
  web_search: "tool.search",
  browser_open: "tool.browse",
  image_generate: "tool.image",
  video_generate: "tool.video",
  memory_add: "tool.remember",
  followup_set: "tool.followup",
  task_progress: "tool.progress",
  note_read: "tool.note",
  computer_apps: "tool.cuApps",
  computer_observe: "tool.cuObserve",
  computer_act: "tool.cuAct",
  computer_act_batch: "tool.cuBatch",
  computer_open: "tool.cuOpen",
  computer_script: "tool.cuScript",
  computer_shortcut: "tool.cuShortcut",
  computer_shortcuts: "tool.cuShortcuts",
};
const toolLabel = (name: string) => (TOOL_LABELS[name] ? t(TOOL_LABELS[name]) : "");
/** tools whose main argument is prose (a task, a prompt), not code */
const PROSE_ARGS = new Set(["delegate", "image_generate", "video_generate", "memory_add", "followup_set", "web_search"]);

/** Start of tools.ParkedResult: the call stopped waiting for its approval. */
const PARKED = "Waiting for the user's approval";

/** Why a request asks, in the UI's language: the category ("删除文件"),
 *  and whether a rule or the default asked; the engine's own wording
 *  ("rules: delete requires approval (default)") is only a tooltip. */
function approvalWhy(req: { category?: string; reason?: string; asked?: string }, time: string): string {
  const cat = req.category ? catLabel(req.category) : "";
  const byRule = !!req.reason && !/\(default\)\s*$/.test(req.reason) && /^rules:/.test(req.reason);
  const when = ago(req.asked || time);
  const waited = when === t("time.justNow") ? t("appr.justAsked") : t("appr.waited", { wait: when });
  return t(byRule ? "appr.byRule" : "appr.byDefault", { cat: cat || req.reason || "", waited });
}

function clip(s: string, n = 6000) {
  return s.length > n ? s.slice(0, n) + `\n… (${s.length - n} more)` : s;
}

function ApprovalCard({ it, agent }: { it: Extract<FeedItem, { k: "approval" }>; agent?: AgentView }) {
  const pending = useStore((s) => s.approvals.some((a) => a.callId === it.req.callId));
  const req = useStore((s) => s.approvals.find((a) => a.callId === it.req.callId)) ?? it.req;
  const name = agent?.name ?? it.req.agent;
  const summary = it.req.summary || it.req.tool;
  if (it.decided) {
    const d = it.decided;
    const by = d.by && d.by !== "desktop" ? d.by + " " : t("common.you");
    const key: Key =
      d.outcome === "allowed-once"
        ? d.remember
          ? "appr.approvedRemember"
          : "appr.approved"
        : d.outcome === "rejected"
          ? d.message
            ? "appr.rejectedWith"
            : "appr.rejected"
          : d.outcome === "cancelled"
            ? "appr.cancelled"
            : "appr.unavailable";
    return (
      <div className={"sys" + (d.outcome === "allowed-once" ? "" : " muted")}>
        {d.outcome === "allowed-once" ? <Check size={14} /> : <CircleX size={14} />}
        <span>{t(key, { by, summary, message: d.message ?? "" })}</span>
      </div>
    );
  }
  const cmd = it.call ? argsPreview(it.call.args) : "";
  return (
    <div className={"qcard" + (pending ? " wait" : "")}>
      {/* the title says what kind of action; the command itself shows once, below */}
      <h4>{t("appr.wants", { agent: name, summary: cmd && toolLabel(it.req.tool) ? toolLabel(it.req.tool) : summary })}</h4>
      <div className="qs">{approvalWhy(it.req, it.time)}</div>
      {cmd && <code className="cmd">{cmd}</code>}
      {req.preview && (
        <details className="apreview">
          <summary>{t("appr.preview")}</summary>
          <Markdown text={req.preview} />
        </details>
      )}
      {/* the friendly tool name (when the title did not already use it); ids only in the tooltip */}
      {toolLabel(it.req.tool) && !cmd && (
        <div className="small" title={[it.req.tool, it.req.category].filter(Boolean).join(" · ")}>
          {toolLabel(it.req.tool)}
        </div>
      )}
      {pending ? <ApprovalActions req={req} /> : <p className="small" style={{ margin: "10px 0 0" }}>{t("appr.expired")}</p>}
    </div>
  );
}

function Compact({ it }: { it: Extract<FeedItem, { k: "compact" }> }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <div className="sys">
        <Minimize2 size={14} />
        <span>{t("chat.compacted", { before: kfmt(it.before), after: kfmt(it.after) })}</span>
        {it.summary && (
          <button className="linkbtn" type="button" onClick={() => setOpen(!open)}>
            {t("chat.compactSummary")}
          </button>
        )}
      </div>
      {open && (
        <div className="brief">
          <Markdown text={it.summary} />
        </div>
      )}
    </>
  );
}

// ---------------------------------------------------------------- composer

interface Mention {
  start: number;
  q: string;
}

/** "@xx" right before the caret (letters, digits, _ and -, like the daemon). */
function mentionAt(text: string, caret: number): Mention | null {
  const m = /(^|\s)@([\p{L}\p{N}_-]*)$/u.exec(text.slice(0, caret));
  if (!m) return null;
  return { start: caret - m[2].length - 1, q: m[2].toLowerCase() };
}

const NO_FILES: never[] = [];

/** How an agent is named in the conversation (a blank chat speaks as 助手). */
function speaker(a: AgentView): string {
  return a.kind === "chat" ? t("chat.assistant") : a.name;
}

function Composer({
  sessionId,
  agent,
  channel,
  ctx,
  lastModel,
}: {
  sessionId: string;
  agent: AgentView;
  channel?: ChannelView;
  ctx?: { tokens: number; window: number; model?: ModelRef };
  lastModel?: ModelRef;
}) {
  const agents = useStore((s) => s.agents);
  const noModels = useStore((s) => hasModels(s) === false);
  const [mention, setMention] = useState<Mention | null>(null);
  const [msel, setMsel] = useState(0);
  const draft = useStore((s) => s.drafts[sessionId] ?? "");
  const override = useStore((s) => s.overrides[agent.id] ?? null);
  const hostConfirm = useStore((s) => s.hostConfirm === agent.id);
  const models = useStore((s) => s.models);
  const composerActions = useSlot("composer.action");
  const manage = useStore((s) => s.tier === "desktop");
  const plain = !channel && agent.kind === "chat";
  const viewer = useStore((s) => s.tier === "viewer");
  const [sending, setSending] = useState(false);
  const pending = useStore((s) => s.attach[sessionId]) ?? NO_FILES;
  const [dragging, setDragging] = useState(false);
  const fileIn = useRef<HTMLInputElement>(null);
  const ta = useRef<HTMLTextAreaElement>(null);
  const hostM = useMenu();
  const permM = useMenu();
  const modelM = useMenu();
  const ctxM = useMenu();
  const dirM = useMenu();

  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 8 * 21 + 10) + "px";
  }, [draft]);

  useEffect(() => {
    ta.current?.focus();
  }, [sessionId]);

  const setDraft = (v: string) => setState((s) => ({ drafts: { ...s.drafts, [sessionId]: v } }));
  const overrideRef = override && !channel ? parseRef(override) : null;
  // in a channel the button is about the agent the draft is for: the
  // first member it @mentions, else the owner (who gets unaddressed messages)
  const target = useMemo(() => {
    if (!channel) return agent;
    for (const m of draft.matchAll(/(?:^|\s)@([\p{L}\p{N}_-]+)/gu)) {
      const want = m[1].toLowerCase();
      const hit = channel.members.map((id) => agents[id]).find((a) => a && (a.id.toLowerCase() === want || a.name.toLowerCase() === want));
      if (hit) return hit;
    }
    return agent;
  }, [channel, draft, agents, agent]);
  const shown = overrideRef ?? activeModel(models, target) ?? (target === agent ? lastModel : undefined) ?? null;
  const prov = shown ? findProvider(models, shown.provider) : undefined;
  const quota = prov?.quota?.known ? Math.round(prov.quota.remaining * 100) : null;
  const pct = ctx && ctx.window > 0 ? Math.min(100, Math.round((ctx.tokens / ctx.window) * 100)) : null;

  const candidates = useMemo(() => {
    if (!channel || !mention) return [];
    return channel.members
      .map((id) => agents[id])
      .filter((a): a is AgentView => !!a && (!mention.q || a.name.toLowerCase().startsWith(mention.q) || a.id.toLowerCase().startsWith(mention.q)));
  }, [channel, mention, agents]);

  const pick = (a: AgentView) => {
    const el = ta.current;
    if (!el || !mention) return;
    const caret = el.selectionStart ?? draft.length;
    // The daemon matches names made of letters, digits, _ and -.
    const handle = /^[\p{L}\p{N}_-]+$/u.test(a.name) ? a.name : a.id;
    const next = draft.slice(0, mention.start) + "@" + handle + " " + draft.slice(caret);
    setDraft(next);
    setMention(null);
    // placed when the new text is rendered, before any further keystroke:
    // a caret set a frame later moved behind letters typed in between
    caretAfter.current = mention.start + handle.length + 2;
  };
  const caretAfter = useRef<number | null>(null);
  useLayoutEffect(() => {
    const el = ta.current;
    const pos = caretAfter.current;
    if (!el || pos === null) return;
    caretAfter.current = null;
    el.focus();
    el.setSelectionRange(pos, pos);
  }, [draft]);

  const archived = channel?.state === "archived";

  // dictation appends to the draft; interim words show live and are
  // replaced by the final transcript
  const [listening, setListening] = useState(false);
  const stopRec = useRef<null | (() => void)>(null);
  const baseDraft = useRef("");
  const toggleDictation = () => {
    if (listening) {
      stopRec.current?.();
      return;
    }
    if (!dictationSupported()) {
      toast(t("voice.unsupported"));
      return;
    }
    baseDraft.current = draft && !/\s$/.test(draft) ? draft + " " : draft;
    setListening(true);
    stopRec.current = dictate(
      (fin, interim) => setDraft(baseDraft.current + fin + interim),
      (error) => {
        setListening(false);
        stopRec.current = null;
        if (error === "denied") toast(t("voice.denied"));
        else if (error && error !== "silence" && error !== "aborted") toast(t("voice.failed", { error }));
        ta.current?.focus();
      },
    );
  };
  useEffect(() => {
    void primeVoice();
    return () => stopRec.current?.();
  }, []);

  const sendKey = (() => {
    const d = display(enterSends() ? "enter" : "mod+enter");
    return MAC ? d.join("") : d.join("+");
  })();
  const canSend = (!!draft.trim() || pending.some((f) => f.state === "ok")) && !pending.some((f) => f.state === "up");
  const busy = !channel && (agent.status === "running" || agent.status === "waiting");

  const attach = (files: File[]) => {
    if (!files.length) return;
    if (channel) {
      toast(t("att.channel"));
      return;
    }
    addFiles(sessionId, agent.id, files);
  };

  const send = async () => {
    const text = draft.trim();
    const ready = pending.filter((f) => f.state === "ok" && f.att);
    if ((!text && !ready.length) || sending || archived) return;
    if (pending.some((f) => f.state === "up")) {
      toast(t("att.waitUpload"));
      return;
    }
    setSending(true);
    if (channel) {
      try {
        const r = await API.postChannel(sessionId, text);
        setDraft("");
        setMention(null);
        const who = agents[r.agent]?.name ?? r.agent;
        toast(r.position > 0 ? t("ch.queued", { name: who, n: r.position }) : t("ch.sentTo", { name: who }));
      } catch (e: any) {
        toast(t("chat.sendFailed", { error: e?.message ?? String(e) }));
      } finally {
        setSending(false);
        ta.current?.focus();
      }
      return;
    }
    try {
      const wasBusy = agent.status === "running" || agent.queue.length > 0;
      await API.send(agent.id, {
        text,
        model: override ?? undefined,
        session: sessionId === agent.session ? undefined : sessionId,
        attachments: ready.length ? ready.map((f) => ({ path: f.att!.path, name: f.att!.name })) : undefined,
      });
      setDraft("");
      clearFiles(sessionId);
      if (wasBusy) toast(t("chat.queued", { n: agent.queue.length + 1 }));
    } catch (e: any) {
      toast(t("chat.sendFailed", { error: e?.message ?? String(e) }));
    } finally {
      setSending(false);
      ta.current?.focus();
    }
  };

  const setMode = async (m: string) => {
    permM.close();
    if (m === "host") {
      setState({ hostConfirm: agent.id });
      return;
    }
    try {
      await API.patchAgent(agent.id, { mode: m });
      toast(t("comp.modeSet", { name: agent.name, mode: modeLabel(m) }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  const confirmHost = async () => {
    try {
      await API.patchAgent(agent.id, { mode: "host" });
      toast(t("comp.hostEnabled", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
    setState({ hostConfirm: null });
  };

  // The pick becomes the first model of the agent the message is for (kept
  // by the daemon: the panel, other devices and a reload agree; the rest of
  // its order stays as fallbacks). 按模型顺序 clears a one-to-one override.
  const setOverride = (r: ModelRef | null) => {
    if (!r) {
      setModelOverride(agent.id, null);
      return;
    }
    if (!channel) setModelOverride(agent.id, null);
    const rest = (target.models ?? []).filter((m) => !(m.provider === r.provider && m.model === r.model));
    void API.patchAgent(target.id, { modelRefs: [refString(r), ...rest.map((m) => refString(m))], session: sessionId })
      .then(() => toast(t("comp.modelSet", { name: target.name, model: shortModelEffort(models, r)?.name ?? r.model })))
      .catch((e: any) => toast(e?.message ?? String(e)));
  };

  const mode = agent.effectiveMode || agent.mode || "sandbox";

  if (viewer)
    return (
      <div className="cwrap">
        <p className="tiernote viewnote">{t("team.viewerComposer")}</p>
      </div>
    );
  return (
    <div className="cwrap">
      {hostConfirm && (
        <div className="confirmbar">
          <p>
            <b>{t("comp.hostConfirm", { name: agent.name })}</b> {t("comp.hostConfirmDesc")}
          </p>
          <button className="btn" type="button" onClick={() => setState({ hostConfirm: null })}>
            {t("comp.hostKeep")}
          </button>
          <button className="btn dng" type="button" onClick={confirmHost}>
            {t("comp.hostOk")}
          </button>
        </div>
      )}
      {noModels && !archived && (
        <div className="confirmbar nomodel">
          <p>
            <b>{t("conn.none")}</b> {t("conn.noneDesc")}
          </p>
          <button className="btn pri" type="button" onClick={openConnect}>
            {t("conn.connect")}
          </button>
        </div>
      )}
      <form
        className={"composer" + (dragging ? " drop" : "")}
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
        onDragOver={(e) => {
          if (archived || !Array.from(e.dataTransfer.types).includes("Files")) return;
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false);
        }}
        onDrop={(e) => {
          if (!Array.from(e.dataTransfer.types).includes("Files")) return;
          e.preventDefault();
          setDragging(false);
          attach(filesFrom(e.dataTransfer));
        }}
      >
        {dragging && <div className="dropmask">{t("att.drop")}</div>}
        <PendingChips session={sessionId} files={pending} />
        {candidates.length > 0 && (
          <div className="mention" role="listbox" aria-label={t("ch.mention")}>
            <div className="mlbl">{t("ch.mention")}</div>
            {candidates.map((a, i) => (
              <button
                key={a.id}
                type="button"
                role="option"
                aria-selected={i === msel % candidates.length}
                className="mi"
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(a);
                }}
              >
                <Avatar agent={a} size={22} status />
                <span className="g">
                  <b>{a.name}</b>
                  <span>
                    {a.role || a.id}
                    {a.id === channel?.owner ? ` · ${t("ch.owner")}` : ""}
                    {a.queue.length ? ` · ${t("ch.queueLen", { n: a.queue.length })}` : ""}
                  </span>
                </span>
              </button>
            ))}
          </div>
        )}
        <textarea
          ref={ta}
          rows={1}
          value={draft}
          disabled={archived}
          placeholder={archived ? t("ch.archivedPh") : channel ? t("chat.placeholderChannel", { name: channel.title || channel.id }) : agent.kind === "chat" ? t("chat.chatPh") : t("chat.placeholder", { name: agent.name })}
          aria-label={channel ? t("chat.placeholderChannel", { name: channel.title || channel.id }) : agent.kind === "chat" ? t("chat.chatPh") : t("chat.placeholder", { name: agent.name })}
          onChange={(e) => {
            setDraft(e.target.value);
            if (channel) {
              const m = mentionAt(e.target.value, e.target.selectionStart ?? e.target.value.length);
              setMention(m);
              setMsel(0);
            }
          }}
          onBlur={() => setMention(null)}
          onPaste={(e) => {
            const files = filesFrom(e.clipboardData);
            if (files.length) {
              e.preventDefault();
              attach(files);
            }
          }}
          onKeyDown={(e) => {
            if (candidates.length > 0) {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                setMsel((i) => (i + (e.key === "ArrowDown" ? 1 : candidates.length - 1)) % candidates.length);
                return;
              }
              if (e.key === "Enter" || e.key === "Tab") {
                e.preventDefault();
                pick(candidates[msel % candidates.length]);
                return;
              }
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                setMention(null);
                return;
              }
            }
            // ⌘Enter sends; with 「Enter 发送」 on, plain Enter sends and
            // Shift+Enter starts a new line
            if (e.key === "Enter" && !e.nativeEvent.isComposing && ((e.metaKey || e.ctrlKey) || (enterSends() && !e.shiftKey && !e.altKey))) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <div className="crow">
          <button
            className="cbtn round"
            type="button"
            aria-label={t("chat.attach")}
            title={channel ? t("att.channel") : t("chat.attach")}
            disabled={archived}
            onClick={() => (channel ? toast(t("att.channel")) : fileIn.current?.click())}
          >
            <Plus size={16} />
          </button>
          <input
            ref={fileIn}
            type="file"
            hidden
            multiple
            accept="image/*,.pdf,.doc,.docx,.ppt,.pptx,.xls,.xlsx,.csv,.txt,.md,.json,.yaml,.yml,.xml,.html,.log,.go,.py,.js,.ts,.tsx,.java,.rs,.c,.cpp,.rb,.php,.sh,.sql"
            onChange={(e) => {
              attach(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
          {/* a blank chat runs nothing: no host, run mode or folder to choose */}
          {!plain && (<>
          <button className="cbtn" type="button" ref={hostM.ref} onClick={hostM.toggle} title={t("comp.host", { name: agent.name })} aria-haspopup="menu" hidden={!manage}>
            <Monitor size={15} />
            <span className="lbl">{t("comp.hostLocal")}</span>
            <ChevronDown size={14} />
          </button>
          <button className={"cbtn" + (mode === "host" ? " danger" : "")} type="button" ref={permM.ref} onClick={permM.toggle} aria-haspopup="menu" disabled={!manage} title={manage ? modeLabel(mode) : t("tier.mobileMode")}>
            {mode === "host" ? <Monitor size={15} /> : <Hand size={15} />}
            <span className="lbl">{modeLabel(mode)}</span>
            <ChevronDown size={14} />
          </button>
          {!channel && (
            <button
              className={"cbtn cdir" + (agent.sharedWorkspace ? " on" : "")}
              type="button"
              ref={dirM.ref}
              onClick={dirM.toggle}
              aria-haspopup="menu"
              disabled={!manage}
              title={t("comp.workdir", { dir: agent.workspace })}
            >
              <Folder size={15} />
              <span className="lbl">{dirName(agent.workspace, !!agent.sharedWorkspace)}</span>
              <ChevronDown size={14} />
            </button>
          )}
          </>)}
          {composerActions.map((c) => (
            <button key={c.key} className="cbtn" type="button" title={c.desc || c.title} onClick={() => void runCommand(c, sessionId)}>
              <SlotIcon name={c.icon} />
              <span className="lbl">{c.title}</span>
            </button>
          ))}
          <span className="sp" />
          <button
            className={"cbtn cmodel" + (override && !channel ? " ov" : "")}
            type="button"
            ref={modelM.ref}
            onClick={modelM.toggle}
            title={channel ? t("ch.modelOf", { name: target.name }) : t("comp.modelTitle")}
            aria-haspopup="menu"
          >
            {shown ? (
              <>
                <ProviderMark provider={shown.provider} model={shown.model} size={12} />
                <span className="lbl">{shortModelEffort(models, shown).name}</span>
                {shortModelEffort(models, shown).effort && <span className="eff">{shortModelEffort(models, shown).effort}</span>}
              </>
            ) : (
              <span className="lbl">{t("na.modelNone")}</span>
            )}
            <ChevronDown size={14} />
          </button>
          <button
            className={"cbtn round ghost" + (listening ? " rec" : "")}
            type="button"
            aria-label={listening ? t("voice.stopDictation") : t("chat.voice")}
            aria-pressed={listening}
            title={!dictationSupported() ? t("voice.unsupported") : listening ? t("voice.stopDictation") : cloudVoice() ? t("voice.cloud") : t("chat.voice")}
            disabled={archived}
            onClick={toggleDictation}
          >
            {listening ? <MicOff size={15} /> : <Mic size={15} />}
          </button>
          {busy && !canSend ? (
            // the agent is working and nothing is typed: the button stops it
            // (typed text still sends: it joins the running turn)
            <button className="send on stop" type="button" onClick={() => runShortcut("stop")} aria-label={t("keys.stop")} title={t("keys.stop") + " · " + keyLabel("stop")}>
              <Square size={12} fill="currentColor" />
            </button>
          ) : (
            <button className={"send" + (canSend ? " on" : "")} type="submit" disabled={!canSend || sending} aria-label={t("chat.send", { key: sendKey })} title={t("chat.send", { key: sendKey })}>
              <ArrowUp size={16} />
            </button>
          )}
        </div>
      </form>
      <div className="cfoot">
        {/* a plain chat has no tools: say so before a link is sent to it */}
        {plain && /https?:\/\/\S/.test(draft) ? <span className="small muted cnote">{t("chat.plainNoLinks")}</span> : <span className="sp" />}
        {shown && quota !== null && (
          <span className={"quota" + (quota <= 10 ? " warnq" : "")}>
            <ProviderMark provider={shown.provider} model={shown.model} size={11} />
            {t("comp.quota", { model: providerName(shown.provider), pct: quota })}
          </span>
        )}
        <button
          className="ctxbtn"
          type="button"
          ref={ctxM.ref}
          onClick={ctxM.toggle}
          aria-haspopup="menu"
          title={pct === null ? t("comp.contextNone") : t("comp.context", { pct })}
          aria-label={pct === null ? t("comp.contextNone") : t("comp.context", { pct })}
        >
          <i className={"ring" + (pct !== null && pct >= 80 ? " hi" : "")} style={{ ["--p" as any]: pct ?? 0 }} />
          {pct ? t("comp.context", { pct }) : null}
        </button>
      </div>

      <Popover anchor={hostM.anchor} open={hostM.open} onClose={hostM.close} placement="top">
        <div className="mlbl">{t("comp.host", { name: agent.name })}</div>
        <button className="mi" type="button" onClick={hostM.close}>
          <Monitor size={16} />
          <span className="g">
            {t("comp.hostLocal")}
            <span>
              <i className="hdot on" />
              {t("comp.hostLocalDesc")}
            </span>
          </span>
          <span className="ck">
            <Check size={14} />
          </span>
        </button>
        <hr />
        <button className="mi" type="button" disabled>
          <Plus size={16} />
          <span className="g">
            {t("comp.hostAdd")}
            <span>{t("common.soon")}</span>
          </span>
        </button>
      </Popover>

      <Popover anchor={dirM.anchor} open={dirM.open} onClose={dirM.close} placement="top" width={340}>
        <DirMenu agent={agent} onDone={dirM.close} />
      </Popover>

      <Popover anchor={permM.anchor} open={permM.open} onClose={permM.close} placement="top" width={300}>
        <div className="mlbl">{t("comp.permTitle", { name: agent.name, host: t("comp.hostLocal") })}</div>
        {(["read-only", "sandbox", "host"] as const).map((m) => (
          <button key={m} className="mi" type="button" onClick={() => setMode(m)}>
            {m === "host" ? <Monitor size={16} /> : <Shield size={16} />}
            <span className="g">
              {modeLabel(m)}
              <span>{t(`mode.${m}.desc` as Key)}</span>
            </span>
            <span className="ck">{mode === m && <Check size={14} />}</span>
          </button>
        ))}
        <hr />
        <p className="small" style={{ margin: "4px 9px 6px" }}>
          {t("comp.permRules")}
        </p>
      </Popover>

      <Popover anchor={modelM.anchor} open={modelM.open} onClose={modelM.close} placement="top" align="end" width={260}>
        <ModelPicker models={models} agent={target} override={overrideRef} shown={shown} setOverride={setOverride} close={modelM.close} />
      </Popover>

      {ctxM.open && <ContextMap agent={agent} sessionId={sessionId} channel={!!channel} ctx={ctx} onClose={ctxM.close} />}
    </div>
  );
}

export function shortPath(p: string): string {
  const m = p.match(/^\/(Users|home)\/[^/]+(\/.*)?$/);
  return m ? "~" + (m[2] ?? "") : p;
}

/** Middle-ellipsis path: keeps the head and the last segment visible. */
/** Label of the working folder: the project's name, or 默认工作区. */
function dirName(p: string, project: boolean): string {
  if (!project) return t("dir.default");
  const parts = p.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || p;
}

const RECENT_KEY = "crewbase.recentDirs";

function recentDirs(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT_KEY) || "[]");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string").slice(0, 6) : [];
  } catch {
    return [];
  }
}

function rememberDir(d: string) {
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify([d, ...recentDirs().filter((x) => x !== d)].slice(0, 6)));
  } catch {
    /* private mode */
  }
}

/** The end of a long path: "…/code/shop". */
function tailPath(p: string, n = 34): string {
  if (p.length <= n) return p;
  const parts = p.split("/");
  let out = parts.pop() ?? "";
  while (parts.length && out.length + parts[parts.length - 1].length + 1 <= n - 2) out = parts.pop() + "/" + out;
  return "…/" + out;
}

/** Folders on the host, one level at a time (no native picker here). */
function DirBrowser({ start, busy, onPick, onBack }: { start: string; busy: boolean; onPick: (dir: string) => void; onBack: () => void }) {
  const [at, setAt] = useState<{ path: string; parent: string; home: string; dirs: { name: string; path: string }[] } | null>(null);
  const [err, setErr] = useState("");
  const seq = useRef(0);
  // fast clicks: only the newest folder counts
  const go = (p?: string) => {
    const n = ++seq.current;
    return API.browseDirs(p).then(
      (r) => {
        if (n !== seq.current) return;
        setAt(r);
        setErr("");
      },
      (e: any) => {
        if (n !== seq.current) return;
        if (e?.status === 404 && p) return void go(); // the folder is gone: start at home
        setErr(e?.status === 403 ? t("dir.browseOwner") : e?.message ?? String(e));
      },
    );
  };
  useEffect(() => {
    void go(start || undefined);
  }, []);
  const atHome = at && at.path === at.home;
  return (
    <div className="dirbrowse">
      <div className="dbhead">
        <button className="ix" type="button" aria-label={t("ap.back")} onClick={onBack}>
          <ChevronLeft size={15} />
        </button>
        <span className="mono g" title={at?.path}>
          {at ? tailPath(shortPath(at.path)) : "…"}
        </span>
        {at && !atHome && (
          <button className="btn sm ghost" type="button" disabled={busy} onClick={() => onPick(at.path)}>
            {t("dir.useHere")}
          </button>
        )}
      </div>
      {err && <p className="small errt" style={{ margin: "6px 9px" }}>{err}</p>}
      <div className="dblist" key={at?.path}>
        {at && !atHome && at.parent && (
          <button className="mi" type="button" onClick={() => void go(at.parent)}>
            <CornerLeftUp size={15} />
            <span className="g">{t("dir.up")}</span>
          </button>
        )}
        {at?.dirs.map((x) => (
          <div key={x.path} className="dbrow">
            <button className="mi" type="button" title={t("dir.open")} onClick={() => void go(x.path)}>
              <Folder size={15} />
              <span className="g">{x.name}</span>
              <ChevronRight size={13} className="muted" />
            </button>
            <button className="btn sm dbuse" type="button" disabled={busy} onClick={() => onPick(x.path)}>
              {t("dir.useShort")}
            </button>
          </div>
        ))}
        {at && at.dirs.length === 0 && <p className="small muted" style={{ margin: "6px 9px" }}>{t("dir.noSub")}</p>}
      </div>
    </div>
  );
}

/** The composer's folder menu: where this agent works (a project folder
 *  or its own workspace). */
function DirMenu({ agent, onDone }: { agent: AgentView; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const d = desktop();
  const project = agent.sharedWorkspace || "";
  const use = async (dir: string) => {
    setBusy(true);
    try {
      const a = await API.setProjectDir(agent.id, dir);
      setState((s) => ({ agents: { ...s.agents, [a.id]: a } }));
      if (dir) rememberDir(a.workspace);
      toast(dir ? t("dir.set", { dir: shortPath(a.workspace) }) : t("dir.reset"));
      onDone();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  // the system's folder picker in the desktop app; elsewhere (a browser,
  // the relay) a folder browser served by the host
  const pick = async () => {
    if (!d?.pickFolder) return setBrowsing(true);
    const dir = await d.pickFolder(project || undefined).catch(() => "");
    if (dir) await use(dir);
  };
  if (browsing) return <DirBrowser start={project || ""} busy={busy} onPick={(dir) => void use(dir)} onBack={() => setBrowsing(false)} />;
  const recents = recentDirs().filter((x) => x !== project);
  return (
    <>
      <div className="mlbl">{t("dir.title", { name: agent.name })}</div>
      <button
        className="mi"
        type="button"
        title={t("comp.copyPath")}
        onClick={() => void navigator.clipboard?.writeText(agent.workspace).then(() => toast(t("comp.copied")), () => {})}
      >
        <Folder size={16} />
        <span className="g">
          {project ? dirName(project, true) : t("dir.default")}
          <span className="mono">{shortPath(agent.workspace)}</span>
        </span>
        <span className="ck">
          <Check size={14} />
        </span>
      </button>
      {recents.map((r) => (
        <button key={r} className="mi" type="button" disabled={busy} onClick={() => void use(r)}>
          <Clock size={16} />
          <span className="g">
            {dirName(r, true)}
            <span className="mono">{shortPath(r)}</span>
          </span>
        </button>
      ))}
      <hr />
      <button className="mi" type="button" disabled={busy} onClick={() => void pick()}>
        <FolderOpen size={16} />
        <span className="g">{t("dir.pick")}</span>
      </button>
      {project && (
        <button className="mi" type="button" disabled={busy} onClick={() => void use("")}>
          <Undo2 size={16} />
          <span className="g">
            {t("dir.back")}
            <span>{t("dir.backDesc")}</span>
          </span>
        </button>
      )}
      <p className="small" style={{ margin: "4px 9px 6px" }}>
        {t("dir.note")}
      </p>
    </>
  );
}

