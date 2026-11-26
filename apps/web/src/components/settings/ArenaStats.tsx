// 个人资料 › 擂台: what the person's arena picks say (features/arena): per
// kind of task, which model wins how often, what it costs and how long it
// takes on average.

import { useEffect, useState } from "react";
import { Swords, Trophy } from "lucide-react";
import { ArenaAPI, parseRef, type ArenaStats as Stats } from "../../api";
import { t } from "../../i18n";
import { costfmt, providerName, shortModel } from "../../format";
import { useStore } from "../../store";
import { modelMark, VendorTile } from "../Avatar";
import { catName } from "../conversation/Arena";

function secs(ms: number) {
  return ms >= 60_000 ? `${Math.round(ms / 60_000)}m` : `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`;
}

export function ArenaStats() {
  const models = useStore((s) => s.models);
  const [st, setSt] = useState<Stats | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [cat, setCat] = useState<string | null>(null);
  useEffect(() => {
    ArenaAPI.stats().then(setSt, (e) => setErr(e?.message ?? String(e)));
  }, []);
  if (err) return <p className="small errt">{err}</p>;
  if (!st) return null;
  if (!st.arenas)
    return (
      <div className="arsnone">
        <Swords size={16} />
        <span className="small">{t("arena.statsNone")}</span>
      </div>
    );
  const cats = st.categories;
  const cur = cats.find((c) => c.category === cat) ?? [...cats].sort((a, b) => b.arenas - a.arenas)[0];
  const top = cur.models[0];
  return (
    <div className="arstats-v">
      <div className="arscats" role="tablist" aria-label={t("arena.statsCats")}>
        {cats.map((c) => (
          <button key={c.category} type="button" role="tab" aria-selected={c === cur} className={"arscat" + (c === cur ? " on" : "")} onClick={() => setCat(c.category)}>
            {catName(c.category)}
            <span className="n">{c.arenas}</span>
          </button>
        ))}
      </div>
      <div className="htable musage arstable">
        <div className="arsum small muted">
          {t("arena.statsSum", { n: cur.arenas, picked: cur.picked })}
          {top && top.wins > 0 && (
            <span className="arslead">
              <Trophy size={12} />
              {t("arena.statsLead", { cat: catName(cur.category), model: (() => { const r = parseRef(top.model); return r ? shortModel(models, r) : top.model; })() })}
            </span>
          )}
        </div>
        {cur.models.map((m) => {
          const r = parseRef(m.model);
          const share = cur.picked ? m.wins / cur.picked : 0;
          return (
            <div key={m.model} className="murow arsrow">
              <VendorTile icon={modelMark(r)} size={22} letter={r ? providerName(r.provider).charAt(0) : "?"} />
              <span className="g">
                <b title={m.model}>{r ? shortModel(models, r) : m.model}</b>
                <span>{t("arena.statsWins", { wins: m.wins, runs: m.runs })}</span>
              </span>
              <span className="mubar" aria-label={t("arena.statsShare", { pct: Math.round(share * 100) })}>
                <i style={{ width: Math.max(m.wins ? 4 : 0, share * 100) + "%" }} />
              </span>
              <span className="num">{secs(m.avgMs)}</span>
              <span className={"num mucost" + (m.priced ? "" : " muted")} title={t("arena.statsCostTip")}>
                {m.priced ? (m.avgCostUsd > 0 ? costfmt(m.avgCostUsd) : t("arena.free")) : t("arena.noPrice")}
              </span>
            </div>
          );
        })}
        {!cur.models.length && <p className="small arsempty">{t("arena.statsNoRuns")}</p>}
      </div>
      <p className="small arslegend">{t("arena.statsLegend")}</p>
    </div>
  );
}
