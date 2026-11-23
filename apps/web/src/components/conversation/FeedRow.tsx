import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ChevronRight, Terminal, AlarmClock, Users, Minimize2, Monitor, Shield, ArrowRightLeft, Link2, UserRound, Pause, Archive, Play } from "lucide-react";
import { parseRef, type AgentView } from "../../api";
import { t, type Key } from "../../i18n";
import { project, type FeedItem, type Step } from "../../projection";
import { loadSession, openConnect, openSubRun, useStore } from "../../store";
import { MediaCard } from "../Media";
import { dayLabel, hhmm, kfmt, modeLabel, modelLabel, shortModelEffort, until, NO_MODEL, MODEL_UNAVAILABLE } from "../../format";
import { Avatar } from "../Avatar";
import { Markdown } from "../Markdown";
import { PluginCard, useSlot } from "../UISlots";
import { speaker } from "./util";
import { groupFeed } from "./feedUtil";
import { UserMsg, AgentMsg, TurnMsg } from "./messages";
import { Steps } from "./steps";
import { ApprovalCard } from "./approval";
import { RetryButton } from "./other";

function PluginFeedCard({ type, data }: { type: string; data: any }) {
  const c = useSlot("conversation.node").find((x) => x.key === type);
  return c ? <PluginCard c={c} data={data} /> : null;
}

export function FeedRow({ it, agents, showModel }: { it: FeedItem; agents: Record<string, AgentView>; showModel?: boolean }) {
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

const REASONS: Record<string, true> = {
  "chat.reason.quota_exhausted": true,
  "chat.reason.rate_limited": true,
  "chat.reason.unavailable": true,
  "chat.reason.model_unavailable": true,
  "chat.reason.model_not_found": true,
};
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
