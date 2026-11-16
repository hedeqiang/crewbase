import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Lock, Pencil } from "lucide-react";
import { API, parseRef, type UsageStats } from "../../api";
import { t, type Key } from "../../i18n";
import { kfmt, modelLabel, providerName } from "../../format";
import { myName, openSession, patchProfile, useStore } from "../../store";
import { Avatar, AvatarRaw, colorFor, MeAvatar } from "../Avatar";
import { ProviderMark } from "../ProviderMark";
import { Head } from "../Settings";


/** d moved by n calendar days (local; DST-safe, unlike adding 24h). */
function addDays(d: Date, n: number) {
  const x = new Date(d);
  x.setDate(x.getDate() + n);
  return x;
}

/** The local calendar day (the daemon counts usage by the host's days). */
function ymd(d: Date) {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** 个人资料 (docs/03 §5.4): name, and usage computed by the daemon from local logs. */
export function Profile() {
  const [u, setU] = useState<UsageStats | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const agents = useStore((s) => s.agents);
  const profile = useStore((s) => s.profile);
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
          <MeAvatar size={44} label={Array.from(myName(profile))[0]} />
          <div>
            <NameEdit current={profile.displayName} shown={myName(profile)} />
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
              <Stat label={t("prof.switches")} value={String(u.switches)} sub={t("prof.interventions", { n: u.interventions })} />
              <Stat
                label={t("prof.saved")}
                value={"$" + u.savedUsd.toFixed(u.savedUsd < 10 ? 2 : 0)}
                sub={t("prof.savedSub", { spent: "$" + u.spentUsd.toFixed(2) })}
                tone={u.savedUsd > 0 ? "ok" : undefined}
              />
            </div>
            <div className="vsec">{t("prof.activity")}</div>
            <Heatmap days={u.days} />
            <div className="vsec">{t("prof.share")}</div>
            <Share u={u} />
            {(u.generated?.length ?? 0) > 0 && (
              <>
                <div className="vsec">{t("prof.generated")}</div>
                <div className="htable">
                  {u.generated!.map((g) => (
                    <div key={g.provider} className="hrow">
                      <ProviderMark provider={g.provider} size={20} />
                      <span className="g">
                        <b>{providerName(g.provider)}</b>
                      </span>
                      <span className="num">{t("prof.generatedN", { i: g.images, v: g.videos })}</span>
                    </div>
                  ))}
                </div>
              </>
            )}
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
                      {a ? <Avatar agent={a} size={28} /> : <AvatarRaw color={colorFor(x.agent)} name={x.name || x.agent} size={28} />}
                      <span className="g">
                        <b>
                          {x.name || a?.name || x.agent}
                          {a?.role && <span className="tag" style={{ marginLeft: 6 }}>{a.role}</span>}
                          {!a && <span className="tag" style={{ marginLeft: 6 }}>{t("prof.deleted")}</span>}
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

/** The person's display name, edited in place. */
function NameEdit({ current, shown }: { current: string; shown: string }) {
  const [edit, setEdit] = useState<string | null>(null);
  const save = () => {
    if (edit !== null && edit.trim() !== current.trim()) void patchProfile({ displayName: edit.trim() });
    setEdit(null);
  };
  if (edit !== null)
    return (
      <input
        className="fi sm profname"
        autoFocus
        maxLength={40}
        value={edit}
        placeholder={t("common.you")}
        aria-label={t("me.rename")}
        onChange={(e) => setEdit(e.target.value)}
        onBlur={save}
        onKeyDown={(e) => {
          if (e.key === "Enter") save();
          if (e.key === "Escape") setEdit(null);
        }}
      />
    );
  return (
    <button type="button" className="profname-btn" title={t("me.rename")} onClick={() => setEdit(current)}>
      <b>{shown}</b>
      <Pencil size={12} />
    </button>
  );
}

function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "ok" }) {
  return (
    <div className={"stat" + (tone ? " " + tone : "")}>
      <span className="sl">{label}</span>
      <b>{value}</b>
      {sub && <span className="small">{sub}</span>}
    </div>
  );
}

function Heatmap({ days }: { days: Record<string, number> }) {
  const summary = useMemo(() => {
    // 53 columns of weeks (Sun..Sat), ending with the current week.
    const today = new Date();
    today.setHours(0, 0, 0, 0);
    const end = addDays(today, 6 - today.getDay());
    const start = addDays(end, -(53 * 7 - 1));
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
        const dt = addDays(start, w * 7 + i);
        const d = ymd(dt);
        const n = days[d] ?? 0;
        col.push({ d, n, lv: level(n), future: dt > today });
        if (i === 0 && dt.getMonth() !== lastMonth) {
          lastMonth = dt.getMonth();
          months.push({ col: w, label: `${dt.getMonth() + 1}${t("prof.month")}` });
        }
      }
      weeks.push(col);
    }
    // streaks and the busiest weekday, over the same window
    let cur = 0;
    let best = 0;
    let run = 0;
    const wd = [0, 0, 0, 0, 0, 0, 0];
    for (let dt = new Date(start); dt <= today; dt = addDays(dt, 1)) {
      const n = days[ymd(dt)] ?? 0;
      if (n > 0) {
        run++;
        best = Math.max(best, run);
        wd[dt.getDay()] += n;
      } else run = 0;
    }
    // the current streak may end yesterday (today has not happened yet)
    for (let dt = new Date(today); ; dt = addDays(dt, -1)) {
      const n = days[ymd(dt)] ?? 0;
      if (n > 0) cur++;
      else if (dt.getTime() !== today.getTime()) break;
      if (dt <= start) break;
    }
    const topWd = wd.some((n) => n > 0) ? wd.indexOf(Math.max(...wd)) : -1;
    return { weeks, levels: cuts, total: vals.length, months, cur, best, topWd };
  }, [days]);
  const { cur, best, topWd } = summary;

  const { weeks, levels, total, months } = summary;
  // Narrow screens: start scrolled to the most recent weeks.
  const scroller = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [weeks]);
  return (
    <div className="box heat">
      <div className="hmain">
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
        <dl className="hsum">
          <div>
            <dt>{t("prof.activeDaysL")}</dt>
            <dd>{total}</dd>
          </div>
          <div>
            <dt>{t("prof.curStreak")}</dt>
            <dd>{t("prof.daysN", { n: cur })}</dd>
          </div>
          <div>
            <dt>{t("prof.bestStreak")}</dt>
            <dd>{t("prof.daysN", { n: best })}</dd>
          </div>
          <div>
            <dt>{t("prof.topWeekday")}</dt>
            <dd>{topWd < 0 ? "—" : t(`prof.wd${topWd}` as Key)}</dd>
          </div>
        </dl>
      </div>
      <div className="hlegend small">
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
    </div>
  );
}
