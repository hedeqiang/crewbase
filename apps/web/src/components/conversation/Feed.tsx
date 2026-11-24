import { jumpTo } from "../MsgExtras";
import { Skeleton } from "../Skeleton";
import { markEntering } from "../../lib/motion";
import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { ChevronRight, ChevronLeft, History } from "lucide-react";
import { type AgentView, type ChannelView } from "../../api";
import { t } from "../../i18n";
import { type FeedItem, type Rewind } from "../../projection";
import { loadSession, setState, useStore, getState, markRead } from "../../store";
import { TakeoverCard, takeoverReason } from "../Browser";
import { detailLabel } from "../../format";
import { Avatar, channelColor, colorFor, ink } from "../Avatar";
import { StreamMarkdown } from "../StreamMarkdown";
import { MsgCtx, SessionCtx } from "./context";
import { speaker, shortPath } from "./util";
import { scrollMemo, FEED_WINDOW, blocks, groupFeed } from "./feedUtil";
import { FeedRow } from "./FeedRow";
import { ChatOpening } from "./other";

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
function Rows({ items, agents, firstNew }: { items: FeedItem[]; agents: Record<string, AgentView>; firstNew?: string | null }) {
  return (
    <>
      {groupFeed(items).map((g) =>
        firstNew && !g.sys && g.items.some((x) => x.it.id === firstNew) ? (
          g.items.map((x) => (
            <Fragment key={x.it.id}>
              {x.it.id === firstNew && <NewDivider />}
              <FeedRow it={x.it} agents={agents} showModel={x.showModel} />
            </Fragment>
          ))
        ) : g.sys ? (
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

/** 新消息: the first reply the person has not seen yet. The first time
 *  the conversation opens on it, it is scrolled into view. */
function NewDivider() {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    const feed = el?.closest(".feed");
    if (!el || !feed) return;
    // only when the new replies start above what is shown
    if (el.getBoundingClientRect().top < feed.getBoundingClientRect().top + 40 || feed.scrollHeight - feed.clientHeight - feed.scrollTop < 4) el.scrollIntoView({ block: "start" });
  }, []);
  return (
    <div className="newdiv" ref={ref} role="separator">
      <span>{t("unread.new")}</span>
    </div>
  );
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

// ---------------------------------------------------------------- feed

export function Feed({
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
  // coming back to a conversation read further up: where it was left
  const restore = useRef<number | null>(scrollMemo.get(sessionId) ?? null);
  const stick = useRef(restore.current === null);
  // 新消息: where the person stopped reading, as it was when they opened it
  const readAtOpen = useRef<number | null>(null);
  const summary = useStore((s) => s.summaries[sessionId]);
  if (readAtOpen.current === null && summary) readAtOpen.current = summary.unread ? summary.read ?? 0 : Infinity;
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
    .filter((id) => !live.some(([k, v]) => k.endsWith("|" + id) && (v.text || v.thinking)))
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
  // the first reply after the read position gets the divider
  const firstNew = useMemo(() => {
    const r = readAtOpen.current;
    if (r === null || r === Infinity) return null;
    const it = items.find((i) => (i.k === "turn" || i.k === "agent") && Number(i.id) > r);
    return it ? it.id : null;
  }, [items, summary !== undefined]);
  // a message found by search: scroll to it once it is here
  const jump = useStore((s) => (s.jump?.session === sessionId ? s.jump : null));
  useEffect(() => {
    if (!jump || loading) return;
    const id = setTimeout(() => {
      stick.current = false;
      jumpTo(jump.seq);
      setState({ jump: null });
    }, 120);
    return () => clearTimeout(id);
  }, [jump, loading, items.length]);
  // reading: at the end of the conversation, with the window in front
  const lastSeq = useStore((s) => {
    const ev = s.events[sessionId];
    return ev && ev.length ? ev[ev.length - 1].seq : 0;
  });
  useEffect(() => {
    if (!lastSeq || loading) return;
    const tick = () => {
      if (document.visibilityState === "visible" && stick.current && getState().current === sessionId && getState().view === "chat") markRead(sessionId, lastSeq);
    };
    tick();
    document.addEventListener("visibilitychange", tick);
    const el = ref.current;
    el?.addEventListener("scroll", tick, { passive: true });
    return () => {
      document.removeEventListener("visibilitychange", tick);
      el?.removeEventListener("scroll", tick);
    };
  }, [sessionId, lastSeq, loading]);
  const anchor = useRef<number | null>(null);
  const showOlder = () => {
    const el = ref.current;
    anchor.current = el ? el.scrollHeight - el.scrollTop : null;
    setShownN((n) => n + FEED_WINDOW);
  };
  const onScroll = () => {
    const el = ref.current;
    if (!el) return;
    stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    if (stick.current) scrollMemo.delete(sessionId);
    else scrollMemo.set(sessionId, el.scrollHeight - el.scrollTop);
  };
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (restore.current !== null && items.length > 0) {
      el.scrollTop = el.scrollHeight - restore.current;
      restore.current = null;
      return;
    }
    if (anchor.current !== null) {
      el.scrollTop = el.scrollHeight - anchor.current;
      anchor.current = null;
      return;
    }
    if (stick.current) el.scrollTop = el.scrollHeight;
  });

  // what is added after the conversation has loaded animates in (lib/motion)
  useEffect(() => {
    const el = ref.current;
    if (!el || loading) return;
    let stop = () => {};
    const id = setTimeout(() => (stop = markEntering(el, sessionId)), 300);
    return () => {
      clearTimeout(id);
      stop();
    };
  }, [sessionId, loading]);

  // your bubbles can take the agent's colour (a channel's own colour)
  const bubbleColor = channel ? channelColor(channel.id) : agent.kind === "chat" ? channelColor(agent.id) : agent.color || colorFor(agent.id);
  return (
    <div
      className="feed"
      ref={ref}
      data-sess={sessionId}
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
      {loading && items.length === 0 && <Skeleton kind="chat" />}
      {!loading && !channel && agent.kind === "chat" && hidden === 0 && <ChatOpening agent={agent} />}
      {!loading && items.length === 0 && !channel && agent.kind !== "chat" && (
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
          <Rows key={"r" + b.items[0].id} items={b.items} agents={agents} firstNew={firstNew} />
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
                <div className="mt streaming">
                  <StreamMarkdown text={v.text} />
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
