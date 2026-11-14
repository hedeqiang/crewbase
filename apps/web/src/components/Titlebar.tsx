import { useMemo } from "react";
import { ListChecks, PanelLeft, PanelRight, Plus, X } from "lucide-react";
import { t } from "../i18n";
import { ago } from "../format";
import { closeTab, openSession, setState, useStore } from "../store";
import { Avatar, AvatarRaw } from "./Avatar";
import { Logo } from "./Logo";
import { ApprovalActions } from "./Approval";
import { takeoverReason } from "./Browser";
import { Popover, useMenu } from "./Popover";

export function Titlebar({ draftColor, draftShape }: { draftColor: string; draftShape: string }) {
  const tabs = useStore((s) => s.tabs);
  const current = useStore((s) => s.current);
  const view = useStore((s) => s.view);
  const sessions = useStore((s) => s.sessions);
  const agents = useStore((s) => s.agents);
  const approvals = useStore((s) => s.approvals);
  const listOpen = useStore((s) => s.listOpen);
  const panelOpen = useStore((s) => s.panelOpen);
  const inbox = useMenu();

  const byAgentSession = useMemo(() => {
    const m: Record<string, (typeof agents)[string]> = {};
    for (const a of Object.values(agents)) m[a.session] = a;
    return m;
  }, [agents]);

  const curAgent = current ? byAgentSession[current] ?? agents[sessions[current]?.agents?.[0] ?? ""] : undefined;
  const takeovers = useMemo(() => Object.values(agents).filter((a) => !!takeoverReason(a)), [agents]);
  const n = approvals.length + takeovers.length;

  return (
    <header className="tbar">
      <button className="ib" type="button" aria-label={t("tb.toggleList")} title={t("tb.toggleList")} aria-pressed={listOpen} onClick={() => setState({ listOpen: !listOpen })}>
        <PanelLeft size={16} />
      </button>
      <div className="brandmark" aria-hidden>
        <Logo size={20} />
      </div>
      <div className="tabs" role="tablist">
        {tabs.map((id) => {
          const a = byAgentSession[id] ?? agents[sessions[id]?.agents?.[0] ?? ""];
          const meta = sessions[id];
          const sel = view === "chat" && current === id;
          return (
            <div
              key={id}
              className="tab"
              role="tab"
              tabIndex={0}
              aria-selected={sel}
              onClick={() => openSession(id)}
              onKeyDown={(e) => e.key === "Enter" && openSession(id)}
              onAuxClick={(e) => e.button === 1 && closeTab(id)}
            >
              {meta?.kind === "channel" ? <span className="hash">#</span> : <Avatar agent={a} size={16} />}
              <span className="tl">
                {meta?.kind === "channel" ? meta.title || id : a?.name ?? meta?.title ?? id}
                {meta?.kind !== "channel" && a?.role && <span className="role"> · {a.role}</span>}
              </span>
              <button
                className="x"
                type="button"
                aria-label={t("common.close")}
                onClick={(e) => {
                  e.stopPropagation();
                  closeTab(id);
                }}
              >
                <X size={12} />
              </button>
            </div>
          );
        })}
        {view === "newAgent" && (
          <div className="tab" aria-selected="true">
            <AvatarRaw color={draftColor} shape={draftShape} size={16} />
            <span className="tl">{t("tb.newAgent")}</span>
            <button className="x" type="button" aria-label={t("common.cancel")} onClick={() => setState({ view: "chat" })}>
              <X size={12} />
            </button>
          </div>
        )}
      </div>
      <div className="curttl">
        {view === "chat" && current && sessions[current]?.kind === "channel" ? (
          <>
            <span className="hash">#</span>
            <span>{sessions[current]?.title || current}</span>
          </>
        ) : (
          view === "chat" &&
          curAgent && (
            <>
              <Avatar agent={curAgent} size={16} />
              <span>{curAgent.name}</span>
            </>
          )
        )}
        {view === "newAgent" && <span>{t("tb.newAgent")}</span>}
        {view === "settings" && <span>{t("tb.settings")}</span>}
      </div>
      <button className="ib" type="button" aria-label={t("tb.new")} title={t("tb.new")} onClick={() => setState({ palette: true })}>
        <Plus size={16} />
      </button>
      <div className="tr">
        <button className={"inboxbtn" + (n ? "" : " clear")} type="button" ref={inbox.ref} onClick={inbox.toggle} aria-haspopup="menu">
          {n ? (
            <>
              {t("tb.needsYou")} <b>{n}</b>
            </>
          ) : (
            <>
              <ListChecks size={15} />
              <span className="lbl">{t("tb.allClear")}</span>
            </>
          )}
        </button>
        <button
          className="ib"
          type="button"
          aria-label={t("tb.togglePanel")}
          title={t("tb.togglePanel")}
          aria-pressed={panelOpen && view === "chat"}
          disabled={view !== "chat"}
          onClick={() => setState({ panelOpen: !panelOpen })}
        >
          <PanelRight size={16} />
        </button>
      </div>
      <Popover anchor={inbox.anchor} open={inbox.open} onClose={inbox.close} align="end" width={360} className="inboxmenu">
        <div className="mlbl">
          {t("tb.needsYou")} · {n}
        </div>
        {n === 0 && <p className="small" style={{ margin: "4px 9px 8px" }}>{t("appr.none")}</p>}
        {takeovers.map((a) => (
          <div key={"tk" + a.id} className="mi top">
            <Avatar agent={a} size={26} status />
            <span className="g">
              <b>{t("br.title", { name: a.name })}</b>
              <span>{takeoverReason(a)}</span>
              <span className="acts sm">
                <button
                  className="btn blue"
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
