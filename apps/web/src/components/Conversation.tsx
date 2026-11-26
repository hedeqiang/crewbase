import { WaitingFor } from "./WaitingFor";
import { NightShiftBar } from "./NightShift";
import { FeedCtx, PinnedBar, SelectionActions } from "./MsgExtras";
import { Skeleton } from "./Skeleton";
import { Welcome } from "./Welcome";
import type { Draft } from "./NewAgent";
import { useEffect, useMemo } from "react";
import { API, type AgentView, type ChannelView } from "../api";
import { t } from "../i18n";
import { project } from "../projection";
import { loadSession, toast, useStore, sessionAgent } from "../store";
import { useSlot } from "./UISlots";
import { type MsgActions, MsgCtx } from "./conversation/context";
import { findMsg } from "./conversation/util";
import { Feed } from "./conversation/Feed";
import { Composer } from "./conversation/Composer";

export function Conversation({ onTemplate }: { onTemplate?: (d: Partial<Draft>) => void } = {}) {
  const current = useStore((s) => s.current);
  const sessions = useStore((s) => s.sessions);
  const agents = useStore((s) => s.agents);
  const loaded = useStore((s) => s.agentsLoaded);
  const sessionsLoaded = useStore((s) => s.sessionsLoaded);
  const routed = useStore((s) => s.routed);
  const channels = useStore((s) => s.channels);
  useEffect(() => {
    if (current) void loadSession(current);
  }, [current]);
  const agent = useMemo(() => sessionAgent(current), [current, sessions, agents, channels]);
  const channel = current ? channels[current] : undefined;

  // until the list has arrived nothing is empty: the shape of a
  // conversation, never the first-run page
  if (!loaded || !sessionsLoaded || (!routed && !agent))
    return (
      <section className="main">
        <div className="feed">
          <Skeleton kind="chat" />
        </div>
      </section>
    );
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
  const feedCtx = useMemo(() => ({ session: sessionId, reactions: proj.reactions, pins: proj.pins }), [sessionId, proj.reactions, proj.pins]);
  // the pinned messages, as they read
  const pinned = useMemo(() => {
    const out: { seq: number; who: string; text: string }[] = [];
    for (const n of proj.pins) {
      const it = findMsg(proj.items, n);
      if (it) out.push(it);
    }
    return out;
  }, [proj.pins, proj.items]);
  return (
    <section className="main">
      <MsgCtx.Provider value={actions}>
        <FeedCtx.Provider value={feedCtx}>
          {oneToOne && sessionId === agent.session && <NightShiftBar agent={agent} />}
          <PinnedBar items={pinned} />
          <Feed sessionId={sessionId} agent={agent} channel={channel} items={proj.items} working={proj.openTurns} loading={!events} error={err} />
          <SelectionActions />
        </FeedCtx.Provider>
      </MsgCtx.Provider>
      <WaitingFor runs={proj.subruns} />
      <Composer sessionId={sessionId} agent={agent} channel={channel} ctx={ctx} lastModel={proj.lastModel[agent.id]} stuck={proj.stuck[agent.id]} />
    </section>
  );
}

// re-exported for the modules that import them from "./Conversation"
export { shortPath } from "./conversation/util";
export { toolLabel } from "./conversation/steps";
export { SubFeed } from "./conversation/FeedRow";
export { splitStarters, Starters } from "./conversation/other";
