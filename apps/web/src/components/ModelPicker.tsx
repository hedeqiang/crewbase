import { useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { Check, ChevronRight, Search } from "lucide-react";
import type { AgentView, ModelRef, ModelsResponse } from "../api";
import { t } from "../i18n";
import { allModels, effortLabel, isExhausted, modelLabel, modelMeta, sameRef, until } from "../format";
import { ProviderMark } from "./ProviderMark";

/** Levels offered in the menu ("" = the model's own default). */
export const EFFORTS = ["", "low", "medium", "high", "xhigh", "max"] as const;

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

type Sub = "model" | "effort" | null;

/**
 * The composer's model menu: two rows, 模型 › and 推理强度 ›, each opening
 * a flyout beside the menu. Every model keeps its own reasoning level.
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
  const [sub, setSub] = useState<Sub>(null);
  const [q, setQ] = useState("");
  const modelRow = useRef<HTMLButtonElement>(null);
  const effortRow = useRef<HTMLButtonElement>(null);
  const order = agent.models ?? [];
  const others = allModels(models).filter((m) => !order.some((r) => sameRef(r, m.ref)));
  const total = order.length + others.length;
  const needle = q.trim().toLowerCase();
  const hit = (...s: (string | undefined)[]) => !needle || s.some((x) => x?.toLowerCase().includes(needle));
  const effort = shown?.effort ?? "";

  const pick = (r: ModelRef | null) => {
    setOverride(r ? withEffort(r) : null);
    close();
  };
  const setEffort = (e: string) => {
    if (!shown) return;
    saveEffort(shown, e);
    setOverride({ provider: shown.provider, model: shown.model, effort: e || undefined });
    close();
  };

  const row = (which: Exclude<Sub, null>, ref: RefObject<HTMLButtonElement | null>, label: string, value: ReactNode, disabled = false) => (
    <button
      ref={ref}
      className={"mi subrow" + (sub === which ? " on" : "")}
      type="button"
      disabled={disabled}
      aria-haspopup="menu"
      aria-expanded={sub === which}
      onMouseEnter={() => !disabled && setSub(which)}
      onClick={() => setSub((s) => (s === which ? null : which))}
      onKeyDown={(e) => {
        if (e.key === "ArrowRight" || e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          setSub(which);
          requestAnimationFrame(() => (document.querySelector(".menu.flyout button") as HTMLElement | null)?.focus());
        }
      }}
    >
      <span className="g">{label}</span>
      <span className="val">{value}</span>
      <ChevronRight size={14} className="chev" />
    </button>
  );

  return (
    <>
      {row(
        "model",
        modelRow,
        t("comp.model"),
        override ? (
          <>
            <ProviderMark provider={override.provider} model={override.model} size={12} />
            {modelLabel(models, override)}
          </>
        ) : (
          t("comp.modelAuto")
        ),
      )}
      {row("effort", effortRow, t("comp.effort"), effort ? effortLabel(effort) : t("effort.default"), !shown)}

      {sub === "model" && (
        <Flyout anchor={modelRow.current} onBack={() => setSub(null)} width={300}>
          {total > 8 && (
            <label className="flsearch">
              <Search size={13} />
              <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("comp.modelSearch")} aria-label={t("comp.modelSearch")} />
            </label>
          )}
          {hit(t("comp.modelAuto")) && (
            <button className="mi" type="button" onClick={() => pick(null)}>
              <span className="g">
                {t("comp.modelAuto")}
                <span>{t("comp.modelAutoDesc")}</span>
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
                      {ex ? ` · ${t("comp.modelExhausted")}${ex.until ? " · " + t("set.quotaResets", { time: until(ex.until) }) : ""}` : ""}
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
        </Flyout>
      )}

      {sub === "effort" && shown && (
        <Flyout anchor={effortRow.current} onBack={() => setSub(null)} width={200}>
          <div className="mlbl" title={modelLabel(models, shown)}>
            {modelLabel(models, shown)}
          </div>
          {EFFORTS.map((e) => (
            <button key={e} className="mi" type="button" aria-pressed={effort === e} onClick={() => setEffort(e)}>
              <span className="g">{e ? effortLabel(e) : t("effort.default")}</span>
              <span className="ck">{effort === e && <Check size={14} />}</span>
            </button>
          ))}
        </Flyout>
      )}
    </>
  );
}

/** A submenu placed beside its row: to the right when it fits, else left. */
function Flyout({ anchor, width, onBack, children }: { anchor: HTMLElement | null; width: number; onBack: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState<{ left: number; top: number; maxH: number } | null>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    const menu = anchor?.closest(".menu") as HTMLElement | null;
    if (!el || !anchor || !menu) return;
    const place = () => {
      const r = anchor.getBoundingClientRect();
      const m = menu.getBoundingClientRect();
      const vw = window.innerWidth;
      const vh = window.innerHeight;
      const w = el.offsetWidth || width;
      const maxH = vh - 16;
      const h = Math.min(el.scrollHeight, maxH);
      let left = m.right + 4;
      if (left + w > vw - 8) left = m.left - w - 4;
      if (left < 8) left = Math.max(8, vw - w - 8); // phone: overlay
      // line the flyout's first item up with the row, but keep it on screen
      let top = r.top - 6;
      if (top + h > vh - 8) top = vh - 8 - h;
      setPos({ left, top: Math.max(8, top), maxH });
    };
    place();
    const ro = new ResizeObserver(place);
    ro.observe(el);
    window.addEventListener("resize", place);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", place);
    };
  }, [anchor, width]);
  return (
    <div
      ref={ref}
      className="menu flyout"
      role="menu"
      style={{ width, left: pos?.left ?? -9999, top: pos?.top ?? -9999, maxHeight: pos?.maxH, visibility: pos ? "visible" : "hidden" }}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft") {
          e.preventDefault();
          onBack();
        }
      }}
    >
      {children}
    </div>
  );
}
