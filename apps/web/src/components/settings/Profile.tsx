import { Skeleton } from "../Skeleton";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Lock, Pencil } from "lucide-react";
import { API, parseRef, type ModelUse, type UsageStats } from "../../api";
import { t, type Key } from "../../i18n";
import { kfmt, modelLabel, providerName, shortModelEffort } from "../../format";
import { myName, patchProfile, useStore } from "../../store";
import { Avatar, MeAvatar } from "../Avatar";
import { ProviderMark } from "../ProviderMark";
import { Head } from "../Settings";
import { ArenaStats } from "./ArenaStats";


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
  const connected = useStore((s) => s.connected);
  useEffect(() => {
    API.usage().then(setU, (e) => setErr(e?.message ?? String(e)));
  }, []);

  return (
    <>
      <Head title={t("set.profile")} sub={t("sub.profile")} />
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
        {!u && !err && <Skeleton />}
        {u && (
          <>
            <div className="stats">
              <Stat label={t("prof.tokens")} value={kfmt(u.tokens)} sub={t("prof.inOut", { i: kfmt(u.inputTokens), o: kfmt(u.outputTokens) })} />
              <Stat label={t("prof.peak")} value={u.peakDay ? u.peakDay.slice(5).replace("-", "/") : "—"} sub={u.peakDay ? t("prof.peakSub", { n: kfmt(u.peakTokens) }) : ""} />
              <Stat label={t("prof.messages")} value={String(u.messages)} />
              <Stat label={t("prof.switches")} value={String(u.switches)} sub={t("prof.interventions", { n: u.interventions })} />
              <Stat
                label={t("prof.spent")}
                value={"$" + u.spentUsd.toFixed(u.spentUsd < 10 ? 2 : 0)}
                sub={(u.gatewayUsd ?? 0) > 0 ? t("prof.viaGateway", { amount: "$" + (u.gatewayUsd ?? 0).toFixed(2) }) : ""}
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
            <div className="vsec">{t("prof.byModel")}</div>
            <ModelTable rows={u.byModel ?? []} />
            <div className="vsec">{t("arena.statsTitle")}</div>
            <ArenaStats />
            <div className="vsec">{t("prof.members")}</div>
            <Members rows={u.byAgent.filter((x) => !x.deleted && agents[x.agent])} />
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

/** What a model's use cost: spent (API keys, gateways), or the kind of
 *  account it ran on when that is not billed per token. */
function costOf(m: { kind: string; spentUsd: number; unpriced?: boolean }): { text: string; muted: boolean } {
  if (m.kind === "api" || m.kind === "gateway") {
    if (m.spentUsd > 0) return { text: usd(m.spentUsd), muted: false };
    return { text: m.unpriced ? t("prof.noPrice") : usd(0), muted: true };
  }
  const k = t(`kind.${m.kind}` as Key);
  return { text: k === `kind.${m.kind}` ? m.kind : k, muted: true };
}

function usd(n: number) {
  if (n > 0 && n < 0.01) return "<$0.01";
  return "$" + n.toFixed(n < 100 ? 2 : 0);
}

/** 模型用量: each model with its tokens (a bar against the busiest), its
 *  replies and what it cost. */
function ModelTable({ rows }: { rows: ModelUse[] }) {
  const models = useStore((s) => s.models);
  const [all, setAll] = useState(false);
  if (!rows.length) return <p className="small">{t("prof.none")}</p>;
  const max = rows[0].tokens || 1;
  const shown = all ? rows : rows.slice(0, 8);
  return (
    <div className="htable musage">
      {shown.map((m) => {
        const r = parseRef(m.model);
        const c = costOf(m);
        return (
          <div key={m.model} className="murow">
            <ProviderMark provider={m.provider} model={r?.model} size={20} />
            <span className="g">
              <b title={m.model}>{r ? modelLabel(models, r) : m.model}</b>
              <span>
                {providerName(m.provider)} · {t("prof.replies", { n: m.messages })} · {t("prof.inOut", { i: kfmt(m.input), o: kfmt(m.output) })}
              </span>
            </span>
            <span className="mubar" aria-hidden>
              <i style={{ width: Math.max(2, (m.tokens / max) * 100) + "%" }} />
            </span>
            <span className="num">{kfmt(m.tokens)}</span>
            <span className={"num mucost" + (c.muted ? " muted" : "")}>{c.text}</span>
          </div>
        );
      })}
      {rows.length > 8 && (
        <button type="button" className="mumore" onClick={() => setAll(!all)}>
          {all ? t("prof.fewer") : t("prof.allModels", { n: rows.length })}
        </button>
      )}
    </div>
  );
}

/** 成员: each member's total, and the models it used with their share. */
function Members({ rows }: { rows: UsageStats["byAgent"] }) {
  const agents = useStore((s) => s.agents);
  const models = useStore((s) => s.models);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  if (!rows.length) return <p className="small">{t("prof.none")}</p>;
  return (
    <div className="mcards">
      {rows.map((x) => {
        const a = agents[x.agent]!;
        const ms = x.models ?? [];
        const shown = open[x.agent] ? ms : ms.slice(0, 3);
        const billed = ms.some((m) => m.kind === "api" || m.kind === "gateway");
        return (
          <div key={x.agent} className="mcard">
            <div className="mchead">
              <Avatar agent={a} size={28} />
              <span className="g">
                <b>
                  {a.name || x.name}
                  {a.role && <span className="tag">{a.role}</span>}
                </b>
                <span>{t("prof.replies", { n: x.messages })} · {t("prof.modelsN", { n: ms.length })}</span>
              </span>
              <span className="mctot">
                <b>{kfmt(x.tokens)}</b>
                <span>{billed ? usd(x.spentUsd) : "\u00a0"}</span>
              </span>
            </div>
            <div className="mcmodels">
              {shown.map((m) => {
                const r = parseRef(m.model);
                const c = costOf(m);
                const pct = x.tokens ? Math.round((m.tokens / x.tokens) * 100) : 0;
                return (
                  <div key={m.model} className="mcm">
                    <ProviderMark provider={m.provider} model={r?.model} size={14} />
                    <span className="mcmn" title={r ? modelLabel(models, r) : m.model}>{r ? shortModelEffort(models, r).name : m.model}</span>
                    <span className="mubar" aria-hidden>
                      <i style={{ width: Math.max(2, pct) + "%" }} />
                    </span>
                    <span className="num">{kfmt(m.tokens)}</span>
                    <span className={"num mucost" + (c.muted ? " muted" : "")}>{c.text}</span>
                  </div>
                );
              })}
              {ms.length > 3 && (
                <button type="button" className="mumore" onClick={() => setOpen((o) => ({ ...o, [x.agent]: !o[x.agent] }))}>
                  {open[x.agent] ? t("prof.fewer") : t("prof.moreModels", { n: ms.length - 3 })}
                </button>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}
