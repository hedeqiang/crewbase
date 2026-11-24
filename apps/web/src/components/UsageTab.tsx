// The right panel's 用量 tab: what this conversation has used, from its
// own log, in place (no popover): the totals and what they cost, how the
// tokens split (input, cache read, cache write, output), the cache hit
// rate and speed, each model's share, and the latest turns.

import { useMemo, useState } from "react";
import type { AgentView } from "../api";
import { costfmt, kfmt, modelLabel } from "../format";
import { t } from "../i18n";
import { useStore } from "../store";
import { cacheHit, sessionUsage, speedOf, type UsageRow } from "../usage";
import { ProviderMark } from "./ProviderMark";

function costText(r: UsageRow): { text: string; note?: string } {
  if (r.cost > 0 || (!r.unpriced && !r.local)) return { text: costfmt(r.cost), note: r.unpriced ? t("ut.partPriced") : undefined };
  if (r.local && !r.unpriced) return { text: t("ut.local") };
  return { text: "—", note: t("ut.noPrice") };
}

/** The tokens, split: fresh input, cache read, cache write, output. */
function Split({ r }: { r: UsageRow }) {
  const fresh = Math.max(0, r.input - r.cacheRead - r.cacheWrite);
  const parts = [
    { k: "fresh", n: fresh, label: t("ut.input") },
    { k: "read", n: r.cacheRead, label: t("ut.cacheRead") },
    { k: "write", n: r.cacheWrite, label: t("ut.cacheWrite") },
    { k: "out", n: r.output, label: t("ut.output") },
  ];
  const all = parts.reduce((a, b) => a + b.n, 0) || 1;
  return (
    <>
      <div className="utbar" role="img" aria-label={parts.map((p) => `${p.label} ${kfmt(p.n)}`).join(" · ")}>
        {parts.map((p) => (p.n ? <i key={p.k} className={"u-" + p.k} style={{ flex: p.n }} title={`${p.label} · ${kfmt(p.n)} · ${Math.round((p.n / all) * 100)}%`} /> : null))}
      </div>
      <div className="utlegend">
        {parts.map((p) => (
          <span key={p.k}>
            <i className={"u-" + p.k} />
            {p.label}
            <b>{kfmt(p.n)}</b>
          </span>
        ))}
      </div>
    </>
  );
}

export function UsageTab({ agent, session }: { agent: AgentView; session: string }) {
  const events = useStore((s) => s.events[session]);
  const models = useStore((s) => s.models);
  const [all, setAll] = useState(false);
  // a channel's conversation is everyone's; an agent's tab is its own
  const u = useMemo(() => sessionUsage(events ?? [], models), [events, models]);
  void agent;
  const r = u.total;
  if (!r.calls)
    return (
      <div className="utab">
        <p className="small muted">{t("ut.none")}</p>
      </div>
    );
  const c = costText(r);
  const hit = cacheHit(r);
  const speed = speedOf(r);
  const turns = all ? u.turns : u.turns.slice(0, 6);
  return (
    <div className="utab">
      <div className="utstats">
        <div>
          <span>{t("ut.tokens")}</span>
          <b>{kfmt(r.input + r.output)}</b>
          <em>{t("ut.calls", { n: r.calls })}</em>
        </div>
        <div>
          <span>{t("ut.cost")}</span>
          <b>{c.text}</b>
          <em>{c.note ?? " "}</em>
        </div>
        <div>
          <span>{t("ut.hit")}</span>
          <b>{hit === null ? "—" : Math.round(hit * 100) + "%"}</b>
          <em>{t("ut.hitSub", { n: kfmt(r.cacheRead) })}</em>
        </div>
        <div>
          <span>{t("ut.speed")}</span>
          <b>{speed === null ? "—" : Math.round(speed) + " tok/s"}</b>
          <em>{t("ut.speedSub", { n: kfmt(r.output) })}</em>
        </div>
      </div>

      <div className="psec">{t("ut.split")}</div>
      <Split r={r} />

      {u.byModel.length > 0 && (
        <>
          <div className="psec">{t("ut.byModel")}</div>
          <div className="utlist">
            {u.byModel.map((m) => {
              const mc = costText(m);
              const share = (m.input + m.output) / (r.input + r.output || 1);
              return (
                <div key={m.model.provider + "/" + m.model.model} className="utrow">
                  <ProviderMark provider={m.model.provider} model={m.model.model} size={16} />
                  <span className="g">
                    <b title={m.model.provider + "/" + m.model.model}>{modelLabel(models, m.model)}</b>
                    <span>
                      {t("ut.calls", { n: m.calls })} · {t("ut.inOut", { i: kfmt(m.input), o: kfmt(m.output) })}
                      {m.cacheRead ? " · " + t("ut.readN", { n: kfmt(m.cacheRead) }) : ""}
                    </span>
                    <span className="utmini">
                      <i style={{ width: Math.max(2, Math.round(share * 100)) + "%" }} />
                    </span>
                  </span>
                  <span className="utc">{mc.text}</span>
                </div>
              );
            })}
          </div>
        </>
      )}

      <div className="psec">{t("ut.turns")}</div>
      <div className="utlist">
        {turns.map((x, i) => {
          const tc = costText(x);
          return (
            <div key={x.time + i} className="utrow turn">
              <span className="g">
                <b title={x.prompt}>{x.prompt || t("ut.noPrompt")}</b>
                <span>
                  {new Date(x.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })} · {t("ut.calls", { n: x.calls })} · {kfmt(x.input + x.output)}
                  {x.cacheRead && x.input ? " · " + t("ut.hitPct", { n: Math.round((x.cacheRead / x.input) * 100) }) : ""}
                </span>
              </span>
              <span className="utc">{tc.text}</span>
            </div>
          );
        })}
        {u.turns.length > 6 && (
          <button type="button" className="utmore" onClick={() => setAll(!all)}>
            {all ? t("ut.fewer") : t("ut.allTurns", { n: u.turns.length })}
          </button>
        )}
      </div>
      <p className="small muted utnote">{t("ut.note")}</p>
    </div>
  );
}
