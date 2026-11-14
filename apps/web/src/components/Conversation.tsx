import { Welcome } from "./Welcome";
import type { Draft } from "./NewAgent";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  CircleX,
  Folder,
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
} from "lucide-react";
import { API, parseRef, refString, type AgentView, type ChannelView, type ContextBreakdown, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { argsFull, argsPreview, project, type FeedItem, type Step } from "../projection";
import { loadSession, openSession, refreshAgents, refreshSessions, setState, toast, useStore, sessionAgent } from "../store";
import { TakeoverCard, takeoverReason } from "./Browser";
import {
  activeModel,
  ago,
  detailLabel,
  allModels,
  dayLabel,
  effortLabel,
  findProvider,
  hhmm,
  isExhausted,
  kfmt,
  modeLabel,
  modelLabel,
  sameRef,
  shortModelEffort,
  until,
} from "../format";
import { Avatar } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { Markdown } from "./Markdown";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";

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
  const proj = useMemo(() => project(events ?? []), [events]);
  const ctx = proj.context[agent.id];
  return (
    <section className="main">
      <Feed sessionId={sessionId} agent={agent} channel={channel} items={proj.items} loading={!events} error={err} />
      <Composer sessionId={sessionId} agent={agent} channel={channel} ctx={ctx} lastModel={proj.lastModel[agent.id]} />
    </section>
  );
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
          <span className="small">{t("chat.emptyDesc", { dir: agent.workspace })}</span>
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
          <span>{detailLabel(agent.detail) || t("chat.thinking", { name: agent.name })}</span>
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
function groupFeed(items: FeedItem[]): { sys: boolean; items: { it: FeedItem; showModel?: boolean }[] }[] {
  const out: { sys: boolean; items: { it: FeedItem; showModel?: boolean }[] }[] = [];
  let prev = "";
  for (const it of items) {
    const sys = isSysItem(it);
    let showModel: boolean | undefined;
    if (it.k === "agent" && it.model) {
      const key = it.model.provider + "/" + it.model.model + "@" + (it.model.effort ?? "");
      showModel = key !== prev;
      prev = key;
    }
    const last = out[out.length - 1];
    if (last && sys && last.sys) last.items.push({ it });
    else out.push({ sys, items: [{ it, showModel }] });
  }
  return out;
}

function FeedRow({ it, agents, showModel }: { it: FeedItem; agents: Record<string, AgentView>; showModel?: boolean }) {
  const models = useStore((s) => s.models);
  switch (it.k) {
    case "time":
      return <div className="tmark">{dayLabel(it.time)}</div>;
    case "user":
      return (
        <div className="m-user">
          <div className="bubble">
            {it.from && <span className="from">{it.from}</span>}
            {it.text}
          </div>
        </div>
      );
    case "agent":
      return <AgentMsg it={it} agent={agents[it.agent]} showModel={!!showModel} />;
    case "steps":
      return <Steps steps={it.steps} />;
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
    case "sys":
      return (
        <div className={"sys " + (it.tone === "error" ? "err" : it.tone)}>
          {it.tone === "error" && <span className="dot err" />}
          <span>{it.key ? t(it.key as Key, it.vars) : it.text}</span>
        </div>
      );
  }
}

const REASONS: Record<string, true> = {
  "chat.reason.quota_exhausted": true,
  "chat.reason.rate_limited": true,
  "chat.reason.unavailable": true,
};

function AgentMsg({ it, agent, showModel }: { it: Extract<FeedItem, { k: "agent" }>; agent?: AgentView; showModel: boolean }) {
  const models = useStore((s) => s.models);
  const [showThink, setShowThink] = useState(false);
  const sm = it.model ? shortModelEffort(models, it.model) : null;
  return (
    <div className="m-agent">
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>
        <div className="mh">
          <b>{agent?.name ?? it.agent}</b>
          <span className="t">{hhmm(it.time)}</span>
          {sm && it.model && (
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
      </div>
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

function Steps({ steps }: { steps: Step[] }) {
  const [open, setOpen] = useState(false);
  const running = steps.some((s) => !s.result && !s.awaiting);
  const awaiting = steps.some((s) => s.awaiting);
  const errors = steps.filter((s) => s.result?.isError).length;
  return (
    <div className="stepsw">
      <button className="row-btn" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {running ? <span className="spin" /> : <Wrench size={14} className="muted" />}
        {running ? t("chat.stepsRunning", { n: steps.length }) : t("chat.steps", { n: steps.length })}
        {awaiting && <span className="pill amber">{t("appr.waiting")}</span>}
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
        {s.awaiting ? <Hand size={13} className="waitc" /> : !s.result ? <span className="spin sm" /> : s.result.isError ? <CircleX size={13} className="errc" /> : <Check size={13} className="okc" />}
        <b className="mono">{s.call.name}</b>
        <span className="mono pv">{pv}</span>
      </button>
      {open && (
        <div className="stepbody">
          <pre className="codeblk">{argsFull(s.call.args)}</pre>
          {s.result && <pre className={"codeblk" + (s.result.isError ? " errblk" : "")}>{clip(s.result.content)}</pre>}
        </div>
      )}
    </div>
  );
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
      <div className="qs">{t("appr.rule", { reason: it.req.reason || it.req.category, wait: ago(it.req.asked || it.time) })}</div>
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
  const [mention, setMention] = useState<Mention | null>(null);
  const [msel, setMsel] = useState(0);
  const draft = useStore((s) => s.drafts[sessionId] ?? "");
  const override = useStore((s) => s.overrides[agent.id] ?? null);
  const hostConfirm = useStore((s) => s.hostConfirm === agent.id);
  const models = useStore((s) => s.models);
  const [sending, setSending] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const hostM = useMenu();
  const permM = useMenu();
  const modelM = useMenu();
  const ctxM = useMenu();

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

  const send = async () => {
    const text = draft.trim();
    if (!text || sending || archived) return;
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
      await API.send(agent.id, { text, model: override ?? undefined, session: sessionId === agent.session ? undefined : sessionId });
      setDraft("");
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

  const setOverride = (r: ModelRef | null) => setState((s) => ({ overrides: { ...s.overrides, [agent.id]: r ? refString(r) : null } }));

  const order = agent.models ?? [];
  const others = allModels(models).filter((m) => !order.some((r) => sameRef(r, m.ref)));
  const mode = agent.effectiveMode || agent.mode || "sandbox";

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
      <form
        className="composer"
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
      >
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
          placeholder={archived ? t("ch.archivedPh") : channel ? t("chat.placeholderChannel", { name: channel.title || channel.id }) : t("chat.placeholder", { name: agent.name })}
          aria-label={channel ? t("chat.placeholderChannel", { name: channel.title || channel.id }) : t("chat.placeholder", { name: agent.name })}
          onChange={(e) => {
            setDraft(e.target.value);
            if (channel) {
              const m = mentionAt(e.target.value, e.target.selectionStart ?? e.target.value.length);
              setMention(m);
              setMsel(0);
            }
          }}
          onBlur={() => setMention(null)}
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
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <div className="crow">
          <button className="cbtn round" type="button" aria-label={t("chat.attach")} title={t("chat.attach")} onClick={() => toast(t("chat.attach"))}>
            <Plus size={16} />
          </button>
          <button className="cbtn" type="button" ref={hostM.ref} onClick={hostM.toggle} title={t("comp.host", { name: agent.name })} aria-haspopup="menu">
            <Monitor size={15} />
            <span className="lbl">{t("comp.hostLocal")}</span>
            <ChevronDown size={14} />
          </button>
          <button className={"cbtn" + (mode === "host" ? " danger" : "")} type="button" ref={permM.ref} onClick={permM.toggle} aria-haspopup="menu">
            {mode === "host" ? <Monitor size={15} /> : <Hand size={15} />}
            <span className="lbl">{modeLabel(mode)}</span>
            <ChevronDown size={14} />
          </button>
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
          <button className="cbtn round ghost" type="button" aria-label={t("chat.voice")} title={t("chat.voice")} onClick={() => toast(t("chat.voice"))}>
            <Mic size={15} />
          </button>
          <button className={"send" + (draft.trim() ? " on" : "")} type="submit" disabled={!draft.trim() || sending} aria-label={t("chat.send")} title={t("chat.send")}>
            <ArrowUp size={16} />
          </button>
        </div>
      </form>
      <div className="cfoot">
        <button
          className="hostlink"
          type="button"
          title={t("comp.workdir", { dir: agent.workspace })}
          onClick={() => {
            void navigator.clipboard?.writeText(agent.workspace).then(() => toast(t("comp.copied")), () => {});
          }}
        >
          <Folder size={13} />
          <MidPath path={shortPath(agent.workspace)} />
        </button>
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
      <Popover anchor={modelM.anchor} open={modelM.open && !channel} onClose={modelM.close} placement="top" align="end" width={320}>
        <div className="mlbl">{t("comp.modelTitle")}</div>
        <button
          className="mi"
          type="button"
          onClick={() => {
            setOverride(null);
            modelM.close();
          }}
        >
          <Check size={16} style={{ opacity: override ? 0 : 1 }} />
          <span className="g">
            {t("comp.modelAuto")}
            <span>{t("comp.modelAutoDesc")}</span>
          </span>
        </button>
        {order.length > 0 && <div className="mlbl">{t("comp.modelOrder", { name: agent.name })}</div>}
        {order.map((r, i) => {
          const ex = isExhausted(models, r);
          return (
            <button
              key={refString(r)}
              className="mi"
              type="button"
              onClick={() => {
                setOverride({ ...r, effort: overrideRef?.effort ?? r.effort });
                modelM.close();
              }}
            >
              <ProviderMark provider={r.provider} model={r.model} size={16} />
              <span className="g">
                {modelLabel(models, r)}
                <span>
                  {i === 0 ? t("comp.modelPrimary") : t("comp.modelBackup")}
                  {r.effort ? ` · ${effortLabel(r.effort)}` : ""}
                  {ex ? ` · ${t("comp.modelExhausted")}${ex.until ? " · " + t("set.quotaResets", { time: until(ex.until) }) : ""}` : ""}
                </span>
              </span>
              <span className="ck">{overrideRef && sameRef(overrideRef, r) && <Check size={14} />}</span>
            </button>
          );
        })}
        {others.length > 0 && <div className="mlbl">{t("comp.modelAll")}</div>}
        {others.map((m) => (
          <button
            key={refString(m.ref)}
            className="mi"
            type="button"
            onClick={() => {
              setOverride({ ...m.ref, effort: overrideRef?.effort });
              modelM.close();
            }}
          >
            <ProviderMark provider={m.ref.provider} model={m.ref.model} size={16} />
            <span className="g">
              {m.info.name || m.info.id}
              <span>
                {m.provider.id}
                {m.info.contextWindow ? ` · ${kfmt(m.info.contextWindow)}` : ""}
              </span>
            </span>
            <span className="ck">{overrideRef && sameRef(overrideRef, m.ref) && <Check size={14} />}</span>
          </button>
        ))}
        <hr />
        <div className="mlbl">{t("comp.effort")}</div>
        <div className="seg">
          {(["", "low", "medium", "high"] as const).map((e) => (
            <button
              key={e}
              type="button"
              aria-pressed={(shown?.effort ?? "") === e}
              disabled={!shown}
              onClick={() => shown && setOverride({ ...shown, effort: e || undefined })}
            >
              {e ? effortLabel(e) : t("effort.default")}
            </button>
          ))}
        </div>
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
function MidPath({ path }: { path: string }) {
  const i = path.lastIndexOf("/");
  if (i <= 0 || path.length < 28) return <span className="path">{path}</span>;
  return (
    <span className="path mid">
      <span className="hd">{path.slice(0, i)}</span>
      <span className="tl">{path.slice(i)}</span>
    </span>
  );
}
