import { useEffect, useState, type ReactNode } from "react";
import { Brain, CalendarCheck, ChevronDown, GitMerge, MessagesSquare, Shapes, ShieldCheck, Wrench } from "lucide-react";
import { MemAPI, type AgentProfile, type AgentView } from "../api";
import { kfmt, usd } from "../format";
import { t, type Key } from "../i18n";
import { useStore } from "../store";
import { toolLabel } from "./Conversation";
import { ProviderMark } from "./ProviderMark";

// 档案: an agent's track record — turns, tools, approvals, models and cost,
// active days, what it does most — and the badges it earned. Counted by the
// daemon as events happen (features/usage profile.go); this only reads it.

const BADGE_ICON: Record<string, ReactNode> = {
  "first-pr": <GitMerge size={12} />,
  "streak-7": <CalendarCheck size={12} />,
  "tools-100": <Wrench size={12} />,
  "clean-week": <ShieldCheck size={12} />,
  "first-memory": <Brain size={12} />,
  "turns-50": <MessagesSquare size={12} />,
  versatile: <Shapes size={12} />,
};
const badgeName = (id: string) => t(`apf.b.${id}` as Key);
const badgeDesc = (id: string) => t(`apf.bd.${id}` as Key);

// one answer per agent, shared by the header marks and the section, kept a
// few seconds (the numbers move only when a turn ends)
const cache = new Map<string, { at: number; p: Promise<AgentProfile> }>();
function fetchProfile(id: string, fresh = false): Promise<AgentProfile> {
  const c = cache.get(id);
  if (c && !fresh && Date.now() - c.at < 5000) return c.p;
  const p = MemAPI.profile(id);
  cache.set(id, { at: Date.now(), p });
  p.catch(() => cache.delete(id));
  return p;
}

export function useProfile(agent: AgentView) {
  const [p, setP] = useState<AgentProfile | null>(null);
  useEffect(() => {
    if (agent.kind === "chat") return;
    let live = true;
    fetchProfile(agent.id).then(
      (r) => live && setP(r),
      () => live && setP(null),
    );
    return () => {
      live = false;
    };
  }, [agent.id, agent.status]);
  return p;
}

/** Earned badges as tiny marks (the name shows on hover or focus). */
export function BadgeMarks({ agent }: { agent: AgentView }) {
  const p = useProfile(agent);
  const earned = p?.badges.filter((b) => b.earned) ?? [];
  if (!earned.length) return null;
  return (
    <span className="apf-marks" aria-label={t("apf.badgesN", { n: earned.length, total: p!.badges.length })}>
      {earned.map((b) => (
        <span key={b.id} className="apf-mark" tabIndex={0} data-tip={badgeName(b.id)} aria-label={badgeName(b.id)}>
          {BADGE_ICON[b.id]}
        </span>
      ))}
    </span>
  );
}

/** The agent panel's 档案. */
export function ProfileSection({ agent }: { agent: AgentView }) {
  const p = useProfile(agent);
  const models = useStore((s) => s.models);
  const [more, setMore] = useState(false);
  if (agent.kind === "chat" || !p) return null;
  const earned = p.badges.filter((b) => b.earned);
  const peak = Math.max(1, ...p.recent);
  const level = (n: number) => (n === 0 ? 0 : Math.min(4, Math.ceil((n / peak) * 4)));
  const nothing = p.turns === 0 && p.toolCalls === 0;
  const topTool = Math.max(1, ...p.tools.map((x) => x.n));
  const modelName = (ref: string) => {
    const [prov, ...rest] = ref.split("/");
    const id = rest.join("/");
    const m = models?.providers.find((x) => x.id === prov)?.models.find((x) => x.id === id);
    return { prov, id, name: m?.name || id };
  };
  return (
    <>
      <div className="psec apf-sech">
        <span>{t("apf.title")}</span>
        {!p.ready && <span className="pn-sech">{t("apf.counting")}</span>}
      </div>
      <div className="apf">
        {nothing ? (
          <div className="small muted apf-empty">{t("apf.empty")}</div>
        ) : (
          <>
            <div className="apf-stats">
              <div className="apf-stat">
                <b>{p.turns}</b>
                <span>{t("apf.turns")}</span>
              </div>
              <div className="apf-stat">
                <b>{p.toolCalls}</b>
                <span>{t("apf.tools")}</span>
              </div>
              <div className="apf-stat" title={t("apf.approvalsHint", { asked: p.asked, ok: p.approved, no: p.rejected })}>
                <b>
                  {p.approved}
                  <i>/</i>
                  <em className={p.rejected ? "red" : ""}>{p.rejected}</em>
                </b>
                <span>{t("apf.approvals")}</span>
              </div>
              <div className="apf-stat" title={t("apf.costHint", { tokens: kfmt(p.tokens) })}>
                <b>{p.spentUsd > 0 ? usd(p.spentUsd) : "—"}</b>
                <span>{t("apf.cost")}</span>
              </div>
            </div>
            <div className="apf-days">
              <div className="apf-strip" role="img" aria-label={t("apf.recentAria", { n: p.recent.filter((x) => x > 0).length })}>
                {p.recent.map((n, i) => (
                  <i key={i} data-l={level(n)} style={{ height: n ? 6 + level(n) * 3 : undefined }} />
                ))}
              </div>
              <span className="small muted">
                {t("apf.active", { n: p.activeDays })}
                {p.streak > 1 ? " · " + t("apf.streak", { n: p.streak }) : ""}
              </span>
            </div>
            {p.strengths.length > 0 && (
              <div className="apf-row">
                <span className="apf-k">{t("apf.goodAt")}</span>
                <span className="apf-chips">
                  {p.strengths.map((s) => (
                    <span key={s.kind} className="apf-chip">
                      {t(`apf.w.${s.kind}` as Key)}
                      <em>{Math.round(s.share * 100)}%</em>
                    </span>
                  ))}
                </span>
              </div>
            )}
          </>
        )}
        <div className="apf-row">
          <span className="apf-k">{t("apf.badges")}</span>
          <span className="apf-chips">
            {earned.length ? (
              earned.map((b) => (
                <span key={b.id} className="apf-badge" title={badgeDesc(b.id)}>
                  {BADGE_ICON[b.id]}
                  {badgeName(b.id)}
                </span>
              ))
            ) : (
              <span className="small muted">{t("apf.noBadges")}</span>
            )}
          </span>
        </div>
        <button className="apf-more" type="button" aria-expanded={more} onClick={() => setMore(!more)}>
          <ChevronDown size={13} className="apf-chev" />
          {more ? t("apf.less") : t("apf.more")}
        </button>
        {more && (
          <div className="apf-detail">
            {p.tools.length > 0 && (
              <>
                <div className="apf-dh">{t("apf.topTools")}</div>
                <div className="apf-bars">
                  {p.tools.slice(0, 6).map((x) => (
                    <div className="apf-bar" key={x.name}>
                      <span className="apf-bn" title={x.name}>
                        {toolLabel(x.name) || x.name}
                      </span>
                      <span className="apf-bt">
                        <i style={{ width: Math.max(4, (x.n / topTool) * 100) + "%" }} />
                      </span>
                      <span className="apf-bv">{x.n}</span>
                    </div>
                  ))}
                </div>
              </>
            )}
            {p.models.length > 0 && (
              <>
                <div className="apf-dh">{t("apf.models")}</div>
                <div className="apf-models">
                  {p.models.slice(0, 4).map((m) => {
                    const n = modelName(m.model);
                    return (
                      <div className="apf-model" key={m.model}>
                        <ProviderMark provider={n.prov} model={n.id} />
                        <span className="apf-mn" title={m.model}>
                          {n.name}
                        </span>
                        <span className="small muted">{t("apf.modelUse", { n: m.messages, tokens: kfmt(m.tokens) })}</span>
                        <span className="apf-mc">{m.spentUsd > 0 ? usd(m.spentUsd) : m.savedUsd > 0 ? t("apf.saved", { usd: usd(m.savedUsd) }) : "—"}</span>
                      </div>
                    );
                  })}
                </div>
              </>
            )}
            <div className="apf-dh">{t("apf.allBadges", { n: earned.length, total: p.badges.length })}</div>
            <div className="apf-grid">
              {p.badges.map((b) => (
                <div key={b.id} className={"apf-cell" + (b.earned ? " got" : "")}>
                  <span className="apf-ci">{BADGE_ICON[b.id]}</span>
                  <span className="apf-ct">
                    <b>{badgeName(b.id)}</b>
                    <span>{b.earned ? (b.at ? t("apf.earnedAt", { day: b.at.slice(5).replace("-", "/") }) : t("apf.earned")) : badgeDesc(b.id)}</span>
                  </span>
                  {!b.earned && b.goal > 1 && (
                    <span className="apf-prog" aria-label={`${b.progress}/${b.goal}`}>
                      <i style={{ width: (b.progress / b.goal) * 100 + "%" }} />
                    </span>
                  )}
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </>
  );
}
