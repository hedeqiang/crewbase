import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Lock } from "lucide-react";
import { API, parseRef, type UsageStats } from "../../api";
import { t, type Key } from "../../i18n";
import { kfmt, modelLabel, providerName } from "../../format";
import { openSession, useStore } from "../../store";
import { Avatar, MeAvatar } from "../Avatar";
import { ProviderMark } from "../ProviderMark";
import { Head } from "../Settings";

const DAY = 86400000;

function ymd(d: Date) {
  return d.toISOString().slice(0, 10);
}

/** 个人资料与用量 (docs/03 §5.4), computed by the daemon from local logs. */
export function Profile() {
  const [u, setU] = useState<UsageStats | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const agents = useStore((s) => s.agents);
  const models = useStore((s) => s.models);
  const connected = useStore((s) => s.connected);
  useEffect(() => {
    API.usage().then(setU, (e) => setErr(e?.message ?? String(e)));
  }, []);

  return (
    <>
      <Head title={t("set.profile")} />
      <div className="vb">
        <div className="profhead">
          <MeAvatar size={44} label={t("common.you")} />
          <div>
            <b>{t("common.you")}</b>
            <span className="small">
              <i className={"hdot" + (connected ? " on" : "")} />
              {t("prof.local")}
            </span>
          </div>
          <span className="small privnote">
            <Lock size={12} />
            {t("prof.private")}
          </span>
        </div>
        {err && <p className="small errt">{err}</p>}
        {!u && !err && <p className="small">{t("common.loading")}</p>}
        {u && (
          <>
            <div className="stats">
              <Stat label={t("prof.tokens")} value={kfmt(u.tokens)} sub={t("prof.inOut", { i: kfmt(u.inputTokens), o: kfmt(u.outputTokens) })} />
              <Stat label={t("prof.peak")} value={u.peakDay ? u.peakDay.slice(5).replace("-", "/") : "—"} sub={u.peakDay ? t("prof.peakSub", { n: kfmt(u.peakTokens) }) : ""} />
              <Stat label={t("prof.messages")} value={String(u.messages)} />
              <Stat label={t("prof.switches")} value={String(u.switches)} sub={t("prof.interventions", { n: u.interventions })} hl />
              <Stat
                label={t("prof.saved")}
                value={"$" + u.savedUsd.toFixed(u.savedUsd < 10 ? 2 : 0)}
                sub={t("prof.savedSub", { spent: "$" + u.spentUsd.toFixed(2) })}
                hl
              />
            </div>
            <div className="vsec">{t("prof.activity")}</div>
            <Heatmap days={u.days} />
            <div className="vsec">{t("prof.share")}</div>
            <Share u={u} />
            <div className="vsec">{t("prof.top")}</div>
            {u.byAgent.length === 0 ? (
              <p className="small">{t("prof.none")}</p>
            ) : (
              <div className="htable">
                {u.byAgent.slice(0, 8).map((x) => {
                  const a = agents[x.agent];
                  const r = parseRef(x.model);
                  return (
                    <button key={x.agent} className="hrow" type="button" disabled={!a} onClick={() => a && openSession(a.session)}>
                      <Avatar agent={a} size={28} />
                      <span className="g">
                        <b>
                          {x.name || a?.name || x.agent}
                          {a?.role && <span className="tag" style={{ marginLeft: 6 }}>{a.role}</span>}
                        </b>
                        <span>
                          {r && <ProviderMark provider={r.provider} model={r.model} size={11} />} {r ? modelLabel(models, r) : x.model}
                        </span>
                      </span>
                      <span className="num">{kfmt(x.tokens)}</span>
                    </button>
                  );
                })}
              </div>
            )}
            <p className="small" style={{ marginTop: 14 }}>
              {t("prof.reference", { i: u.reference.input, o: u.reference.output })}
            </p>
          </>
        )}
      </div>
    </>
  );
}

function Stat({ label, value, sub, hl }: { label: string; value: string; sub?: string; hl?: boolean }) {
  return (
    <div className={"stat" + (hl ? " hl" : "")}>
      <span className="sl">{label}</span>
      <b>{value}</b>
      {sub && <span className="small">{sub}</span>}
    </div>
  );
}

function Heatmap({ days }: { days: Record<string, number> }) {
  const { weeks, levels, total, months } = useMemo(() => {
    // 53 columns of weeks (Sun..Sat), ending with the current week.
    const today = new Date();
    today.setUTCHours(0, 0, 0, 0);
    const end = new Date(today.getTime() + (6 - today.getUTCDay()) * DAY);
    const start = new Date(end.getTime() - (53 * 7 - 1) * DAY);
    const vals = Object.values(days).filter((n) => n > 0);
    // Five steps: 0 = none, then four equal bands of the busiest day.
    const max = Math.max(0, ...vals);
    const cuts = [max / 4, max / 2, (max * 3) / 4];
    const level = (n: number) => (n <= 0 ? 0 : Math.min(4, Math.max(1, Math.ceil((n / max) * 4))));
    const weeks: { d: string; n: number; lv: number; future: boolean }[][] = [];
    const months: { col: number; label: string }[] = [];
    let lastMonth = -1;
    for (let w = 0; w < 53; w++) {
      const col = [];
      for (let i = 0; i < 7; i++) {
        const dt = new Date(start.getTime() + (w * 7 + i) * DAY);
        const d = ymd(dt);
        const n = days[d] ?? 0;
        col.push({ d, n, lv: level(n), future: dt > today });
        if (i === 0 && dt.getUTCMonth() !== lastMonth) {
          lastMonth = dt.getUTCMonth();
          months.push({ col: w, label: `${dt.getUTCMonth() + 1}${t("prof.month")}` });
        }
      }
      weeks.push(col);
    }
    return { weeks, levels: cuts, total: vals.length, months };
  }, [days]);

  // Narrow screens: start scrolled to the most recent weeks.
  const scroller = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [weeks]);
  return (
    <div className="box heat">
      <div className="hmscroll" ref={scroller}>
        <div className="hmonths">
          {months.map((m) => (
            <span key={m.col} style={{ gridColumn: m.col + 1 }}>
              {m.label}
            </span>
          ))}
        </div>
        <div className="hgrid" role="img" aria-label={t("prof.activityAria", { n: total })}>
          {weeks.map((col, w) => (
            <div className="hcol" key={w}>
              {col.map((c) => (
                <i key={c.d} className={"hc l" + c.lv + (c.future ? " fut" : "")} title={c.future ? undefined : `${c.d} · ${kfmt(c.n)} tokens`} />
              ))}
            </div>
          ))}
        </div>
      </div>
      <div className="hlegend small">
        <span>{t("prof.activeDays", { n: total })}</span>
        <span className="sp" />
        {t("prof.less")}
        {[0, 1, 2, 3, 4].map((l) => (
          <i key={l} className={"hc l" + l} title={l === 0 ? "0" : l < 4 ? `≤ ${kfmt(levels[l - 1])}` : `> ${kfmt(levels[2])}`} />
        ))}
        {t("prof.more")}
      </div>
    </div>
  );
}

function Share({ u }: { u: UsageStats }) {
  const total = u.byProvider.reduce((a, b) => a + b.tokens, 0);
  if (!total) return <p className="small">{t("prof.none")}</p>;
  // Fixed categorical order by provider rank; >7 providers fold into "other".
  const top = u.byProvider.slice(0, 7);
  const rest = u.byProvider.slice(7).reduce((a, b) => a + b.tokens, 0);
  const rows = [...top.map((p, i) => ({ ...p, slot: i + 1 })), ...(rest ? [{ provider: "", kind: "", tokens: rest, slot: 8 }] : [])];
  return (
    <div className="box">
      <div className="sharebar" role="img" aria-label={t("prof.share")}>
        {rows.map((p) => (
          <i key={p.provider || "_"} style={{ flex: p.tokens, background: `var(--cat-${p.slot})` }} title={`${p.provider ? providerName(p.provider) : t("prof.other")} · ${Math.round((p.tokens / total) * 100)}%`} />
        ))}
      </div>
      <div className="sharelist">
        {rows.map((p) => (
          <div key={p.provider || "_"}>
            <i style={{ background: `var(--cat-${p.slot})` }} />
            {p.provider && <ProviderMark provider={p.provider} size={16} />}
            <span className="g">
              {p.provider ? providerName(p.provider) : t("prof.other")}
              {p.kind && <span className="small"> · {t(`kind.${p.kind}` as Key) === `kind.${p.kind}` ? p.kind : t(`kind.${p.kind}` as Key)}</span>}
            </span>
            <span className="num">{kfmt(p.tokens)}</span>
            <span className="num pct">{Math.round((p.tokens / total) * 100)}%</span>
          </div>
        ))}
      </div>
      {Object.keys(u.kinds).length > 0 && (
        <p className="small" style={{ margin: "10px 0 0" }}>
          {Object.entries(u.kinds)
            .map(([k, n]) => `${t(`kind.${k}` as Key) === `kind.${k}` ? k : t(`kind.${k}` as Key)} ${kfmt(n)}`)
            .join(" · ")}
        </p>
      )}
    </div>
  );
}
