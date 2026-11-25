import { useMemo } from "react";
import { PanelLeft, PanelRight, ShieldCheck } from "lucide-react";
import { t } from "../i18n";
import { ago } from "../format";
import { chatTitle, isChat, openSession, sessionAgent, setState, useStore } from "../store";
import { Avatar, AvatarRaw } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { takeoverReason } from "./Browser";
import { Popover, useMenu } from "./Popover";
import { desktop } from "../api";

/** Toggle for the session list; lives in the sidebar's title-bar area or, when
 *  the sidebar is hidden, at the left of the conversation header. */
export function ListToggle({ className = "" }: { className?: string }) {
  const listOpen = useStore((s) => s.listOpen);
  return (
    <button
      className={"ib nodrag " + className}
      type="button"
      aria-label={t("tb.toggleList")}
      title={t("tb.toggleList")}
      aria-pressed={listOpen}
      onClick={() => setState({ listOpen: !listOpen })}
    >
      <PanelLeft size={16} />
    </button>
  );
}

/** Pending approvals + browser takeovers: the "需要你" count. */
export function useNeedsYou() {
  const agents = useStore((s) => s.agents);
  const approvals = useStore((s) => s.approvals);
  const takeovers = useMemo(() => Object.values(agents).filter((a) => !!takeoverReason(a)), [agents]);
  return { approvals, takeovers, n: approvals.length + takeovers.length };
}

/** The 44px strip on top of the main column: who this conversation is with,
 *  the 需要你 pill (only when something waits) and the right-panel toggle. */
export function ConvHeader({ draftColor, draftShape, draftFace }: { draftColor: string; draftShape: string; draftFace?: string }) {
  const current = useStore((s) => s.current);
  const view = useStore((s) => s.view);
  const sessions = useStore((s) => s.sessions);
  const agents = useStore((s) => s.agents);
  const channels = useStore((s) => s.channels);
  const summaries = useStore((s) => s.summaries);
  const panelOpen = useStore((s) => s.panelOpen);
  const approvals = useStore((s) => s.approvals);
  const { takeovers, n } = useNeedsYou();
  const inbox = useMenu();

  const meta = current ? sessions[current] : undefined;
  const isChannel = meta?.kind === "channel";
  const agent = useMemo(() => sessionAgent(current), [current, sessions, agents, channels]);
  const ch = current ? channels[current] : undefined;
  const owner = ch ? agents[ch.owner] : undefined;

  let state: { cls: string; text: string } | null = null;
  if (view === "chat" && agent && !isChannel) {
    const tk = takeoverReason(agent);
    const pend = approvals.filter((r) => r.agent === agent.id);
    // only questions from servers: 等你回答
    if (pend.length) state = { cls: "amber", text: t(pend.every((r) => r.elicit) ? "el.waiting" : "hd.waitApprove") };
    else if (tk) state = { cls: "amber", text: t("hd.waitTakeover") };
    else if (agent.status === "running") state = { cls: "blue", text: t("hd.running") };
    else if (agent.paused) state = { cls: "muted", text: t("hd.paused") };
    else if (agent.status === "failed") state = { cls: "red", text: t("hd.failed") };
    else if (!isChat(agent)) state = { cls: "idle", text: t("status.idle") };
  }

  return (
    <header className={"chead" + (view === "chat" && !agent ? " bare" : "") + (desktop() ? "" : " web")}>
      <ListToggle className="lt" />
      <div className="who">
        {view === "chat" && isChannel && (
          <>
            <span className="hash">#</span>
            <b className="nm">{meta?.title || current}</b>
            {owner && (
              <span className="ownchip">
                <Avatar agent={owner} size={16} />
                {t("hd.owner")} {owner.name}
              </span>
            )}
            {ch?.settings?.handoverConfirm && (
              <span className="ownchip confirm" title={t("ch.handoverConfirmDesc")}>
                <ShieldCheck size={12} />
                {t("ch.confirmTag")}
              </span>
            )}
          </>
        )}
        {view === "chat" && !isChannel && agent && (
          <>
            <Avatar agent={agent} size={24} />
            <b className="nm">{isChat(agent) ? chatTitle(agent, summaries[agent.session]?.topic) : agent.name}</b>
            {isChat(agent) ? <span className="role">{t("chat.blankTag")}</span> : agent.role && <span className="role">{agent.role}</span>}
            {state && (
              <span className={"hst " + state.cls}>
                <i />
                {state.text}
              </span>
            )}
          </>
        )}
        {view === "newAgent" && (
          <>
            <AvatarRaw color={draftColor} shape={draftShape} face={draftFace} size={24} />
            <b className="nm">{t("tb.newAgent")}</b>
          </>
        )}
      </div>
      <div className="tr">
        {n > 0 && (
          <button className="inboxbtn nodrag" type="button" ref={inbox.ref} onClick={inbox.toggle} aria-haspopup="menu">
            {t("tb.needsYou")} <b>{n}</b>
          </button>
        )}
        <button
          className="ib nodrag"
          type="button"
          aria-label={t("tb.togglePanel")}
          title={t("tb.togglePanel")}
          aria-pressed={panelOpen && view === "chat"}
          disabled={view !== "chat" || !agent}
          onClick={() => setState({ panelOpen: !panelOpen })}
        >
          <PanelRight size={16} />
        </button>
      </div>
      <Popover anchor={inbox.anchor} open={inbox.open && n > 0} onClose={inbox.close} align="end" width={360} className="inboxmenu">
        <div className="mlbl">
          {t("tb.needsYou")} · {n}
        </div>
        {takeovers.map((a) => (
          <div key={"tk" + a.id} className="mi top">
            <Avatar agent={a} size={26} status />
            <span className="g">
              <b>{t("br.title", { name: a.name })}</b>
              <span>{takeoverReason(a)}</span>
              <span className="acts sm">
                <button
                  className="btn pri"
                  type="button"
                  onClick={() => {
                    inbox.close();
                    openSession(a.session);
                  }}
                >
                  {t("tb.goTakeover")}
                </button>
              </span>
            </span>
          </div>
        ))}
        {approvals.map((r) => {
          const a = agents[r.agent];
          return (
            <div key={r.callId} className="mi top">
              <Avatar agent={a} size={26} status />
              <span className="g">
                <b>{t("appr.wants", { agent: a?.name ?? (r.agent || t("appr.workflow")), summary: r.summary || r.tool })}</b>
                <span>
                  「{r.reason || r.category}」 · {ago(r.asked)}
                </span>
                <ApprovalActions
                  req={r}
                  small
                  extra={
                    <button
                      className="btn ghost"
                      type="button"
                      onClick={() => {
                        inbox.close();
                        openSession(r.session);
                      }}
                    >
                      {t("appr.open")}
                    </button>
                  }
                />
              </span>
            </div>
          );
        })}
      </Popover>
    </header>
  );
}
