import { useState } from "react";
import { Check, Search } from "lucide-react";
import type { AgentView, ModelRef, ModelsResponse } from "../api";
import { t } from "../i18n";
import { allModels, effortLabel, exhaustedHint, exhaustedLabel, findModel, isExhausted, modelLabel, modelMeta, modelOff, sameRef } from "../format";
import { ProviderMark } from "./ProviderMark";

/** Levels offered in the menu ("" = the model's own default). */
export const EFFORTS = ["", "low", "medium", "high", "xhigh", "max"] as const;

/** The levels to offer for a model: those it takes (always with 默认),
 *  every level when the provider does not say, none when it takes none. */
export function effortsFor(info: { efforts?: string[] | null } | undefined): string[] {
  const e = info?.efforts;
  if (e == null) return [...EFFORTS];
  if (e.length === 0) return [];
  return ["", ...EFFORTS.filter((x) => x && e.includes(x))];
}

const KEY = "crewbase.modelEfforts";
const plain = (r: ModelRef) => `${r.provider}/${r.model}`;

function readEfforts(): Record<string, string> {
  try {
    return JSON.parse(localStorage.getItem(KEY) || "{}") ?? {};
  } catch {
    return {};
  }
}

/** The reasoning level this person last chose for a model, if any. */
export function savedEffort(r: ModelRef): string | undefined {
  const v = readEfforts()[plain(r)];
  return v === undefined ? undefined : v;
}

function saveEffort(r: ModelRef, e: string) {
  try {
    const all = readEfforts();
    all[plain(r)] = e;
    localStorage.setItem(KEY, JSON.stringify(all));
  } catch {
    /* private window: the choice still applies to this chat */
  }
}

/** A model with its remembered level (or the level it came with). */
function withEffort(r: ModelRef): ModelRef {
  const s = savedEffort(r);
  const e = s === undefined ? r.effort : s;
  return { provider: r.provider, model: r.model, effort: e || undefined };
}


/**
 * The composer's model menu, one panel: the reasoning level of the model in
 * use as a segmented control at the top, then the models (自动 first,
 * showing which one it uses now, the agent's order, then all). One click
 * picks; every model keeps its own reasoning level.
 */
export function ModelPicker({
  models,
  agent,
  override,
  shown,
  setOverride,
  close,
}: {
  models: ModelsResponse | null;
  agent: AgentView;
  override: ModelRef | null;
  shown: ModelRef | null;
  setOverride: (r: ModelRef | null) => void;
  close: () => void;
}) {
  const [q, setQ] = useState("");
  // models switched off in Settings › Models are not offered
  const order = (agent.models ?? []).filter((r) => !modelOff(models, r));
  const others = allModels(models).filter((m) => !order.some((r) => sameRef(r, m.ref)));
  const total = order.length + others.length;
  const needle = q.trim().toLowerCase();
  const hit = (...s: (string | undefined)[]) => !needle || s.some((x) => x?.toLowerCase().includes(needle));
  // the level of the model in use: what is saved for it, else its own
  const effort = shown ? (savedEffort(shown) ?? shown.effort ?? "") : "";
  const levels = shown ? effortsFor(findModel(models, shown)) : [];

  const pick = (r: ModelRef | null) => {
    setOverride(r ? withEffort(r) : null);
    close();
  };
  const setEffort = (e: string) => {
    if (!shown) return;
    saveEffort(shown, e);
    // a level belongs to a model: in 自动 this picks the model in use, at
    // that level (the menu says so; 自动 is one click away)
    setOverride({ provider: shown.provider, model: shown.model, effort: e || undefined });
  };

  return (
    <div className="mpick">
      {levels.length > 0 && shown && (
        <div className="mpeff" title={modelLabel(models, shown)}>
          <span className="mpl">{t("comp.effort")}</span>
          <div className="seg mpseg" role="radiogroup" aria-label={t("comp.effort")}>
            {levels.map((e) => (
              <button key={e || "_"} type="button" role="radio" aria-checked={effort === e} className={effort === e ? "on" : ""} onClick={() => setEffort(e)}>
                {e ? effortLabel(e) : t("effort.default")}
              </button>
            ))}
          </div>
        </div>
      )}
      {total > 8 && (
        <label className="flsearch">
          <Search size={13} />
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("comp.modelSearch")} aria-label={t("comp.modelSearch")} />
        </label>
      )}
      <div className="mplist">
        {hit(t("comp.modelAuto")) && (
          <button className="mi" type="button" onClick={() => pick(null)}>
            <span className="g">
              {t("comp.modelAuto")}
              <span>{!override && shown ? t("comp.modelAutoNow", { model: modelLabel(models, shown) }) : t("comp.modelAutoDesc")}</span>
            </span>
            <span className="ck">{!override && <Check size={14} />}</span>
          </button>
        )}
        {order.some((r) => hit(r.model, modelLabel(models, r))) && <div className="mlbl">{t("comp.modelOrder", { name: agent.name })}</div>}
        {order
          .filter((r) => hit(r.model, modelLabel(models, r)))
          .map((r) => {
            const ex = isExhausted(models, r);
            const e = withEffort(r).effort;
            return (
              <button key={plain(r)} className="mi" type="button" onClick={() => pick(r)}>
                <ProviderMark provider={r.provider} model={r.model} size={16} />
                <span className="g">
                  {modelLabel(models, r)}
                  <span>
                    {order[0] && sameRef(order[0], r) ? t("comp.modelPrimary") : t("comp.modelBackup")}
                    {e ? ` · ${effortLabel(e)}` : ""}
                    {ex ? ` · ${exhaustedLabel(ex)}${ex.until ? " · " + exhaustedHint(ex) : ""}` : ""}
                  </span>
                </span>
                <span className="ck">{override && sameRef(override, r) && <Check size={14} />}</span>
              </button>
            );
          })}
        {others.some((m) => hit(m.info.id, m.info.name, m.provider.id)) && <div className="mlbl">{t("comp.modelAll")}</div>}
        {others
          .filter((m) => hit(m.info.id, m.info.name, m.provider.id))
          .map((m) => {
            const e = withEffort(m.ref).effort;
            return (
              <button key={plain(m.ref)} className="mi" type="button" onClick={() => pick(m.ref)}>
                <ProviderMark provider={m.ref.provider} model={m.ref.model} size={16} />
                <span className="g">
                  {m.info.name || m.info.id}
                  <span>
                    {m.provider.id}
                    {modelMeta(m.info) ? ` · ${modelMeta(m.info)}` : ""}
                    {e ? ` · ${effortLabel(e)}` : ""}
                  </span>
                </span>
                <span className="ck">{override && sameRef(override, m.ref) && <Check size={14} />}</span>
              </button>
            );
          })}
      </div>
    </div>
  );
}
