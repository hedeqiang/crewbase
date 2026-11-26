import { useRef, useState } from "react";
import type React from "react";
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
 * The composer's model choice, shown in place above the text box: the
 * reasoning level of the model in use (a row of levels), then the models,
 * searchable: the agent's own order first, then everything else. Every
 * model keeps its own reasoning level.
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
  const listRef = useRef<HTMLDivElement>(null);
  // models switched off in Settings › Models are not offered
  const order = (agent.models ?? []).filter((r) => !modelOff(models, r));
  const others = allModels(models).filter((m) => !order.some((r) => sameRef(r, m.ref)));
  const total = order.length + others.length;
  const needle = q.trim().toLowerCase();
  const hit = (...s: (string | undefined)[]) => !needle || s.some((x) => x?.toLowerCase().includes(needle));
  const effort = shown?.effort ?? "";
  const levels = shown ? effortsFor(findModel(models, shown)) : [];

  const pick = (r: ModelRef | null) => {
    setOverride(r ? withEffort(r) : null);
    close();
  };
  // a level applies at once and the choice stays open (a model may follow)
  const setEffort = (e: string) => {
    if (!shown) return;
    saveEffort(shown, e);
    setOverride({ provider: shown.provider, model: shown.model, effort: e || undefined });
  };
  const keys = (e: React.KeyboardEvent) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const rows = Array.from(listRef.current?.querySelectorAll<HTMLElement>("button.mi:not(:disabled)") ?? []);
    if (!rows.length) return;
    e.preventDefault();
    const i = rows.indexOf(document.activeElement as HTMLElement);
    const next = e.key === "ArrowDown" ? (i < 0 ? 0 : Math.min(rows.length - 1, i + 1)) : i <= 0 ? -1 : i - 1;
    if (next < 0) listRef.current?.parentElement?.querySelector<HTMLElement>(".flsearch input")?.focus();
    else rows[next].focus();
  };
  const mark = (on: boolean) => <span className="ck">{on && <Check size={14} />}</span>;

  return (
    <div className="mtray" onKeyDown={keys}>
      {/* a model that takes no reasoning level (most media and
          non-reasoning models) has no such choice */}
      {levels.length > 0 && shown && (
        <div className="mpsec">
          <div className="mlbl">
            {t("comp.effort")}
            <span className="mpfor" title={modelLabel(models, shown)}>
              {modelLabel(models, shown)}
            </span>
          </div>
          <div className="seg mpeff" role="group" aria-label={t("comp.effort")}>
            {levels.map((e) => (
              <button key={e} type="button" aria-pressed={effort === e} onClick={() => setEffort(e)}>
                {e ? effortLabel(e) : t("effort.default")}
              </button>
            ))}
          </div>
        </div>
      )}
      <div className="mpsec">
        <div className="mlbl">{t("comp.model")}</div>
        {total > 8 && (
          <label className="flsearch">
            <Search size={13} />
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("comp.modelSearch")} aria-label={t("comp.modelSearch")} />
          </label>
        )}
        <div className="mplist" ref={listRef}>
          {hit(t("comp.modelAuto")) && (
            <button className="mi" type="button" onClick={() => pick(null)}>
              <span className="g">
                {t("comp.modelAuto")}
                <span>{t("comp.modelAutoDesc")}</span>
              </span>
              {mark(!override)}
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
                  {mark(!!override && sameRef(override, r))}
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
                  {mark(!!override && sameRef(override, m.ref))}
                </button>
              );
            })}
        </div>
      </div>
    </div>
  );
}
