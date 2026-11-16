import { Welcome } from "./Welcome";
import { runShortcut } from "../actions";
import { keyLabel } from "../shortcuts";
import { catLabel } from "./Settings";
import type { Draft } from "./NewAgent";
import { createContext, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
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
  Hash as TokensIcon,
} from "lucide-react";
import { display, enterSends, MAC } from "../shortcuts";
import { API, desktop, parseRef, refString, type AgentView, type ChannelView, type ContextBreakdown, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { argsFull, argsPreview, project, type FeedItem, type Rewind, type Step, type TurnStats } from "../projection";
import { hasModels, loadSession, setModelOverride, openConnect, openSession, openSubRun, refreshAgents, refreshSessions, setState, toast, useStore, sessionAgent } from "../store";
import { TakeoverCard, takeoverReason } from "./Browser";
import { cloudVoice, dictate, dictationSupported, primeVoice, speak, speaking, speakSupported } from "../voice";
import { addFiles, clearFiles, filesFrom, MsgAttachments, PendingChips } from "./Attachments";
import { MediaCard } from "./Media";
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
} from "../format";
import { Avatar } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { Markdown } from "./Markdown";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";
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
        <Feed sessionId={sessionId} agent={agent} channel={channel} items={proj.items} loading={!events} error={err} />
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
  }) + (u.estimated ? " · " + t("msg.estimated") : "");
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
            <TokensIcon size={11} aria-hidden />
            {u.estimated ? "≈" : ""}
            {tokfmt(total)}
          </span>
          {u.cost !== undefined && (
            <span className="rs-cost" title={t("msg.costTitle")}>
              {costfmt(u.cost)}
            </span>
          )}
          {u.billing && (
            <span className="rs-cost free" title={t(u.billing === "local" ? "msg.localTitle" : "msg.subTitle")}>
              {t(u.billing === "local" ? "msg.local" : "msg.subscription")}
            </span>
          )}
        </span>
      )}
    </div>
  );
}

/** A user message: copy, and edit and resend in a one-to-one conversation. */
function UserMsg({ it }: { it: Extract<FeedItem, { k: "user" }> }) {
  const acts = useContext(MsgCtx);
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
        <div className="bubble">
          {it.from && <span className="from">{it.from}</span>}
          {it.text}
        </div>
      )}
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
  const n = rows.filter((x) => x.k === "user" || x.k === "agent").length;
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
              g.items.map((x) => <FeedRow key={x.it.id} it={x.it} agents={agents} showModel={x.showModel} before={x.before} after={x.after} />),
            )}
          </MsgCtx.Provider>
        </div>
      )}
    </div>
  );
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
  loading,
  error,
}: {
  sessionId: string;
  agent: AgentView;
  channel?: ChannelView;
  items: FeedItem[];
  loading: boolean;
  error?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const streams = useStore((s) => s.streams);
  const agents = useStore((s) => s.agents);
  const live = Object.entries(streams).filter(([k]) => k.startsWith(sessionId + "|"));
  const waitingApproval = useStore((s) => s.approvals.some((a) => a.agent === agent.id && a.session === sessionId));
  const typing = agent.status === "running" && !waitingApproval && !live.some(([k, v]) => k.endsWith("|" + agent.id) && v.text);
  // Agents in this conversation that wait for a person to take over their browser.
  const takeovers = useMemo(() => {
    const ids = channel ? channel.members : [agent.id];
    const pendingCall = new Set<string>();
    for (const it of items) if (it.k === "steps") for (const st of it.steps) if (st.call.name === "browser_request_takeover" && !st.result) pendingCall.add(it.agent);
    return ids
      .map((id) => agents[id])
      .filter(
        (a): a is AgentView =>
          !!a && !!takeoverReason(a) && (a.statusSession ? a.statusSession === sessionId : pendingCall.has(a.id) || (!channel && a.session === sessionId)),
      );
  }, [items, agents, channel, agent.id, sessionId]);

  const onScroll = () => {
    const el = ref.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  return (
    <div className="feed" ref={ref} onScroll={onScroll}>
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
          <b>{t("chat.empty", { name: agent.name })}</b>
          {/* the role says what it is for; a folder only when the person chose one */}
          {agent.role && agent.kind !== "chat" && <span className="small">{agent.role}</span>}
          <span className="small">{agent.sharedWorkspace ? t("chat.emptyInDir", { dir: shortPath(agent.sharedWorkspace) }) : t("chat.emptyDesc")}</span>
        </div>
      )}
      {!loading && channel && !items.some((i) => i.k === "user" || i.k === "agent") && (
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
      {segments(items).map((seg) =>
        seg.sup ? (
          <Superseded key={"sup" + seg.items[0].id} rw={seg.sup} rows={seg.items} agents={agents} />
        ) : (
          groupFeed(seg.items).map((g) =>
            g.sys ? (
              <div className="sysgrp" key={g.items[0].it.id}>
                {g.items.map((x) => (
                  <FeedRow key={x.it.id} it={x.it} agents={agents} />
                ))}
              </div>
            ) : (
              g.items.map((x) => <FeedRow key={x.it.id} it={x.it} agents={agents} showModel={x.showModel} before={x.before} after={x.after} />)
            ),
          )
        ),
      )}
      {live.map(([k, v]) => {
        const a = agents[k.split("|")[1]];
        if (!v.text && !v.thinking) return null;
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
      {typing && (
        <div className="typing">
          <Avatar agent={agent} size={24} />
          <span>{detailLabel(agent.detail) || t("chat.thinking", { name: speaker(agent) })}</span>
          <span className="dots">
            <i />
            <i />
            <i />
          </span>
        </div>
      )}
    </div>
  );
}

const SYS_KINDS = new Set<FeedItem["k"]>(["switch", "mode", "compact", "handover", "owner", "chstate", "sys"]);

function isSysItem(it: FeedItem): boolean {
  return SYS_KINDS.has(it.k) || (it.k === "approval" && !!it.decided);
}

/** Split the feed into runs; consecutive system lines become one compact group.
 *  Agent messages show their model only when it differs from the previous one. */
type Row = { it: FeedItem; showModel?: boolean; before?: Step[]; after?: Step[] };

/** Tool steps belong to the agent's message they lead to (or follow):
 *  they are shown inside it, under its name, instead of floating between
 *  messages. Steps with no message next to them (still working) get the
 *  agent's header of their own. */
function joinSteps(items: FeedItem[]): Row[] {
  const out: Row[] = [];
  let carry: Step[] | null = null;
  for (let i = 0; i < items.length; i++) {
    const it = items[i];
    if (it.k === "steps") {
      const next = items[i + 1];
      const prev = out[out.length - 1];
      if (next?.k === "agent" && next.agent === it.agent) carry = [...(carry ?? []), ...it.steps];
      else if (prev?.it.k === "agent" && prev.it.agent === it.agent) prev.after = [...(prev.after ?? []), ...it.steps];
      else out.push({ it });
      continue;
    }
    if (it.k === "agent" && carry) {
      out.push({ it, before: carry });
      carry = null;
      continue;
    }
    out.push({ it });
  }
  return out;
}

function groupFeed(items: FeedItem[]): { sys: boolean; items: Row[] }[] {
  const out: { sys: boolean; items: Row[] }[] = [];
  let prev = "";
  for (const row of joinSteps(items)) {
    const it = row.it;
    const sys = isSysItem(it);
    let showModel: boolean | undefined;
    if (it.k === "agent" && it.model) {
      const key = it.model.provider + "/" + it.model.model + "@" + (it.model.effort ?? "");
      showModel = key !== prev;
      prev = key;
    }
    const last = out[out.length - 1];
    if (last && sys && last.sys) last.items.push({ it });
    else out.push({ sys, items: [{ ...row, showModel }] });
  }
  return out;
}

function PluginFeedCard({ type, data }: { type: string; data: any }) {
  const c = useSlot("conversation.node").find((x) => x.key === type);
  return c ? <PluginCard c={c} data={data} /> : null;
}

function FeedRow({ it, agents, showModel, before, after }: { it: FeedItem; agents: Record<string, AgentView>; showModel?: boolean; before?: Step[]; after?: Step[] }) {
  const models = useStore((s) => s.models);
  switch (it.k) {
    case "time":
      return <div className="tmark">{dayLabel(it.time)}</div>;
    case "user":
      return <UserMsg it={it} />;
    case "agent":
      return <AgentMsg it={it} agent={agents[it.agent]} showModel={!!showModel} before={before} after={after} />;
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
          {it.tone === "error" && NO_MODEL.test(it.text) ? (
            <>
              <span>{t("chat.noModelErr")}</span>
              <button className="linkbtn" type="button" onClick={openConnect}>
                {t("conn.connect")}
              </button>
            </>
          ) : it.tone === "error" && CLI_WONT_START.test(it.text) ? (
            <span title={it.text}>{t("chat.cliWontStart")}</span>
          ) : (
            <span>{it.key ? t(it.key as Key, it.vars) : it.text}</span>
          )}
        </div>
      );
  }
}

/** A subscription CLI's program could not be started (EBADMACHO -88: a
 *  binary for another CPU or a broken download; ENOENT; permission). */
const CLI_WONT_START = /spawn (Unknown system error -88|E(NOENT|ACCES|BADMACHO))|Bad CPU type|exec format error/i;

const REASONS: Record<string, true> = {
  "chat.reason.quota_exhausted": true,
  "chat.reason.rate_limited": true,
  "chat.reason.unavailable": true,
};

function AgentMsg({ it, agent, showModel, before, after }: { it: Extract<FeedItem, { k: "agent" }>; agent?: AgentView; showModel: boolean; before?: Step[]; after?: Step[] }) {
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
    <div className={"m-agent" + (it.stats ? " final" : "")}>
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>
        <div className="mh">
          <b>{agent ? speaker(agent) : it.agent}</b>
          <span className="t">{hhmm(it.time)}</span>
          {/* the turn's last reply names its model in the footer */}
          {sm && it.model && !it.stats && (
            <span className={"via" + (showModel ? "" : " hov")} title={modelLabel(models, it.model)}>
              {sm.name}
              {sm.effort && <span className="effs">{sm.effort}</span>}
            </span>
          )}
        </div>
        {it.thinking && (
          <button className="row-btn think" type="button" aria-expanded={showThink} onClick={() => setShowThink(!showThink)}>
            {t("chat.thoughts")}
            <ChevronRight size={14} className="ch" />
          </button>
        )}
        {showThink && <div className="thinking small">{it.thinking}</div>}
        {before && <Steps steps={before} />}
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
        {after && <Steps steps={after} />}
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
        g.items.map((x) => <FeedRow key={x.it.id} it={x.it} agents={agents} before={x.before} after={x.after} />),
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
        <Steps steps={steps} />
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

function Steps({ steps }: { steps: Step[] }) {
  const [open, setOpen] = useState(false);
  const running = steps.some((s) => !s.result && !s.awaiting);
  const awaiting = steps.some((s) => s.awaiting);
  const errors = steps.filter((s) => outcome(s) === "error").length;
  const declined = steps.filter((s) => outcome(s) === "declined").length;
  // one step needs no "ran 1 step" wrapper: the step itself is the line
  if (steps.length === 1)
    return (
      <div className="stepsw one">
        <StepRow s={steps[0]} />
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
            <StepRow key={s.call.id} s={s} />
          ))}
        </div>
      )}
    </div>
  );
}

function StepRow({ s }: { s: Step }) {
  const [open, setOpen] = useState(false);
  const pv = argsPreview(s.call.args);
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
        {toolLabel(s.call.name) ? <b>{toolLabel(s.call.name)}</b> : <b className="mono">{s.call.name}</b>}
        <span className={"pv" + (PROSE_ARGS.has(s.call.name) ? "" : " mono")}>{pv}</span>
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
      {open && (
        <div className="stepbody">
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
      <h4>{t("appr.wants", { agent: name, summary })}</h4>
      <div className="qs">{approvalWhy(it.req, it.time)}</div>
      {cmd && <code className="cmd">{cmd}</code>}
      {req.preview && (
        <details className="apreview">
          <summary>{t("appr.preview")}</summary>
          <Markdown text={req.preview} />
        </details>
      )}
      <div className="small">
        {it.req.tool}
        {it.req.category ? ` · ${it.req.category}` : ""}
      </div>
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
  const shown = overrideRef ?? activeModel(models, agent) ?? lastModel ?? null;
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
    const pos = mention.start + handle.length + 2;
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(pos, pos);
    });
  };

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

  const setOverride = (r: ModelRef | null) => setModelOverride(agent.id, r ? refString(r) : null);

  const order = agent.models ?? [];
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
          <button className="cbtn" type="button" ref={hostM.ref} onClick={hostM.toggle} title={t("comp.host", { name: agent.name })} aria-haspopup="menu" hidden={!manage}>
            <Monitor size={15} />
            <span className="lbl">{t("comp.hostLocal")}</span>
            <ChevronDown size={14} />
          </button>
          <button className={"cbtn" + (mode === "host" ? " danger" : "")} type="button" ref={permM.ref} onClick={permM.toggle} aria-haspopup="menu" disabled={!manage} title={manage ? undefined : t("tier.mobileMode")}>
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
            title={channel ? t("ch.modelOwner", { name: agent.name }) : t("comp.modelTitle")}
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
        <span className="sp" />
        {shown && quota !== null && (
          <span className={"quota" + (quota <= 10 ? " warnq" : "")}>
            <ProviderMark provider={shown.provider} model={shown.model} size={11} />
            {t("comp.quota", { model: shortModelEffort(models, shown).name, pct: quota })}
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

      <Popover anchor={modelM.anchor} open={modelM.open && !!channel} onClose={modelM.close} placement="top" align="end" width={300}>
        <div className="mlbl">{t("ch.modelOwner", { name: agent.name })}</div>
        {order.map((r, i) => (
          <div key={refString(r)} className="mi">
            <ProviderMark provider={r.provider} model={r.model} size={16} />
            <span className="g">
              {modelLabel(models, r)}
              <span>{i === 0 ? t("comp.modelPrimary") : t("comp.modelBackup")}</span>
            </span>
          </div>
        ))}
        <hr />
        <p className="small" style={{ margin: "4px 9px 6px" }}>
          {t("ch.modelNote")}
        </p>
      </Popover>
      <Popover anchor={modelM.anchor} open={modelM.open && !channel} onClose={modelM.close} placement="top" align="end" width={260}>
        <ModelPicker models={models} agent={agent} override={overrideRef} shown={shown} setOverride={setOverride} close={modelM.close} />
      </Popover>

      <Popover anchor={ctxM.anchor} open={ctxM.open} onClose={ctxM.close} placement="top" align="end" width={320}>
        <ContextPopover agent={agent} sessionId={sessionId} channel={!!channel} ctx={ctx} close={ctxM.close} />
      </Popover>
    </div>
  );
}

const SEG_COLORS: Record<string, string> = {
  system: "var(--seg-system)",
  memory: "var(--seg-memory)",
  tools: "var(--seg-tools)",
  skills: "var(--seg-skills)",
  toolio: "var(--seg-toolio)",
  messages: "var(--seg-messages)",
  free: "var(--active)",
};

export function ContextPopover({
  agent,
  sessionId,
  channel,
  ctx,
  close,
}: {
  agent: AgentView;
  sessionId: string;
  channel: boolean;
  ctx?: { tokens: number; window: number; model?: ModelRef };
  close: () => void;
}) {
  const models = useStore((s) => s.models);
  const [bd, setBd] = useState<ContextBreakdown | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<"" | "compact" | "new">("");
  const load = () =>
    API.context(agent.id, sessionId)
      .then((b) => {
        setBd(b);
        setErr(null);
      })
      .catch((e) => setErr(e?.message ?? String(e)));
  useEffect(() => {
    void load();
  }, [agent.id, sessionId]);

  const window_ = bd?.window || ctx?.window || 0;
  const used = bd?.used ?? ctx?.tokens ?? 0;
  const pct = window_ ? Math.round((used / window_) * 100) : null;
  const thr = Math.round((bd?.threshold || 0.8) * 100);
  const model = bd?.model?.provider ? bd.model : ctx?.model;

  const compact = async () => {
    setBusy("compact");
    try {
      await API.compact(agent.id, sessionId);
      toast(t("ctx.compacted", { name: agent.name }));
      await load();
    } catch (e: any) {
      toast(t("ctx.compactFailed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy("");
    }
  };
  const fresh = async () => {
    setBusy("new");
    try {
      const m = await API.newConversation(agent.id);
      await Promise.allSettled([refreshAgents(), refreshSessions()]);
      close();
      openSession(m.id);
      toast(t("ctx.newChatDone", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      <div className="ctxp">
        <div className="ch">
          <b>{t("ctx.title")}</b>
          <span>{pct === null ? "—" : t("ctx.used", { pct })}</span>
        </div>
        {pct !== null ? (
          <>
            <div className="cbar">
              {bd
                ? bd.segments
                    .filter((g) => g.tokens > 0)
                    .map((g) => <i key={g.key} title={`${segLabel(g.key, g.label)} · ${kfmt(g.tokens)}`} style={{ flex: Math.max(g.tokens, window_ * 0.004), background: SEG_COLORS[g.key] ?? "var(--muted)" }} />)
                : [
                    <i key="u" style={{ flex: Math.max(pct, 1), background: "var(--accent)" }} />,
                    <i key="f" style={{ flex: Math.max(100 - pct, 0.001), background: "var(--active)" }} />,
                  ]}
              <span className="thr" style={{ left: thr + "%" }} title={thr + "%"} />
            </div>
            <div className="small">
              {t("ctx.tokens", { used: kfmt(used), total: kfmt(window_), model: model ? modelLabel(models, model) : "—" })}
            </div>
            {bd && (
              <div className="clist">
                {bd.segments.map((g) => (
                  <div key={g.key}>
                    <i style={{ background: SEG_COLORS[g.key] ?? "var(--muted)" }} />
                    <span>{segLabel(g.key, g.label)}</span>
                    <b>{kfmt(g.tokens)}</b>
                  </div>
                ))}
              </div>
            )}
            {bd?.overThreshold && <p className="small warnline" style={{ margin: "8px 0 0" }}>{t("ctx.over")}</p>}
            {bd && bd.compactions > 0 && <p className="small" style={{ margin: "6px 0 0" }}>{t("ctx.compactions", { n: bd.compactions })}</p>}
          </>
        ) : (
          <p className="small" style={{ margin: 0 }}>
            {err ?? t("ctx.noData")}
          </p>
        )}
        <p className="small" style={{ margin: "8px 0 0" }}>
          {t("ctx.note")}
        </p>
      </div>
      <hr />
      <button className="mi" type="button" disabled={!!busy || !bd} onClick={compact}>
        {busy === "compact" ? <span className="spin sm" /> : <Minimize2 size={16} />}
        <span className="g">
          {t("ctx.compact")}
          <span>{t("ctx.compactDesc")}</span>
        </span>
      </button>
      {!channel && (
        <button className="mi" type="button" disabled={!!busy} onClick={fresh}>
          {busy === "new" ? <span className="spin sm" /> : <Plus size={16} />}
          <span className="g">
            {t("ctx.newChat")}
            <span>{t("ctx.newChatDesc", { name: agent.name })}</span>
          </span>
        </button>
      )}
    </>
  );
}

function segLabel(key: string, fallback: string): string {
  const k = `ctx.seg.${key}` as Key;
  const v = t(k);
  return v === k ? fallback : v;
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
          <button className="linkbtn" type="button" disabled={busy} onClick={() => onPick(at.path)}>
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

