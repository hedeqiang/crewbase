import { useMemo } from "react";
import { Plus } from "lucide-react";
import { API, refString, type ModelRef, type ModelsResponse } from "../api";
import { t, type Key } from "../i18n";
import { allModels, sameRef, shortModel } from "../format";
import { currentTeam, teamMode } from "../store";
import { COLORS, SHAPES, VendorTile, modelMark } from "./Avatar";
import { markName, vendorFamily } from "./ProviderMark";

/** 模型圆桌: one agent per chosen model, each wearing its model's mark
 *  (跟随模型), all in one channel. The person picks the models (one row
 *  per maker that is connected; a maker can take more than one seat) and
 *  may rename them before anything is created. */

export interface Seat {
  key: string;
  /** the maker (vendorFamily of the model's mark) */
  family: string;
  ref: ModelRef;
  name: string;
  /** the name was typed by the person (kept when the model changes) */
  edited: boolean;
}

export interface Maker {
  family: string;
  /** a mark for the row before a model is chosen */
  mark: string;
  models: { ref: ModelRef; label: string }[];
}

/** at most this many seats; the first few makers are picked to start */
export const ROUNDTABLE_MAX = 8;
export const ROUNDTABLE_DEFAULT = 4;

// makers in the order the default picks them
const PREFER = ["anthropic", "openai", "google", "deepseek", "moonshot", "qwen", "bytedance", "zhipu", "xai", "minimax", "mistral", "meta"];
// a maker's strongest model first (ids as providers list them)
const STRONG = ["claude-opus", "claude-sonnet", "gpt-6.2", "gpt-6.1", "gpt-6", "gemini-3", "gemini-2.5-pro", "deepseek-v4-pro", "kimi-k2", "qwen3", "glm-5", "glm-4", "grok-4"];

/** The product name a member of this maker goes by (editable). */
function productName(family: string, mark: string): string {
  switch (family) {
    case "anthropic":
      return "Claude";
    case "openai":
      return "ChatGPT";
    case "google":
      return "Gemini";
    case "moonshot":
      return "Kimi";
    case "xai":
      return "Grok";
    case "meta":
      return "Llama";
    case "qwen":
    case "bytedance":
    case "zhipu":
    case "hunyuan":
    case "wenxin":
    case "spark":
      return t(`rt.vn.${family}` as Key);
  }
  return markName(mark);
}

/** Connected chat models by maker, in the order the default prefers. */
export function makersOf(models: ModelsResponse | null, defaults: string[] = []): Maker[] {
  const by = new Map<string, Maker>();
  for (const m of allModels(models)) {
    const mark = modelMark(m.ref);
    if (!mark) continue;
    const family = vendorFamily(mark);
    let mk = by.get(family);
    if (!mk) by.set(family, (mk = { family, mark, models: [] }));
    mk.models.push({ ref: m.ref, label: m.info.name || m.info.id });
  }
  const rank = (r: ModelRef) => {
    const s = refString(r, false);
    const d = defaults.findIndex((x) => x.split("@")[0] === s);
    if (d >= 0) return d;
    const i = STRONG.findIndex((w) => r.model.toLowerCase().includes(w));
    return i >= 0 ? 100 + i : 1000;
  };
  for (const mk of by.values()) {
    mk.models.sort((a, b) => rank(a.ref) - rank(b.ref));
    mk.mark = modelMark(mk.models[0].ref) ?? mk.mark;
  }
  const order = (f: string) => {
    const i = PREFER.indexOf(f);
    return i >= 0 ? i : PREFER.length;
  };
  return [...by.values()].sort((a, b) => order(a.family) - order(b.family) || a.family.localeCompare(b.family));
}

/** Default names: the maker's product name, told apart by the model's
 *  short name when two seats share a maker ("Claude Opus", "Claude Sonnet"). */
export function nameSeats(models: ModelsResponse | null, seats: Seat[]): Seat[] {
  const named = seats.map((s) => {
    if (s.edited) return s;
    const mark = modelMark(s.ref) ?? s.family;
    const base = productName(s.family, mark);
    const shared = seats.filter((x) => x.family === s.family).length > 1;
    if (!shared) return { ...s, name: base };
    const short = shortModel(models, s.ref);
    // "Claude Opus 5.5" → "Claude Opus"; "GPT-6.2 Sol" stays as it is
    const words = short.split(/\s+/);
    const name = words[0].toLowerCase() === base.toLowerCase() && words.length > 1 ? words.slice(0, 2).join(" ") : short;
    return { ...s, name: Array.from(name).slice(0, 24).join("") };
  });
  // the same model twice (through two gateways): "Claude Opus", "Claude Opus 2"
  return named.map((s, i) => {
    if (s.edited) return s;
    const n = named.slice(0, i).filter((x) => x.name === s.name).length;
    return n ? { ...s, name: `${Array.from(s.name).slice(0, 21).join("")} ${n + 1}` } : s;
  });
}

/** The starting table: the strongest model of the first makers. */
export function defaultSeats(models: ModelsResponse | null, makers: Maker[]): Seat[] {
  return nameSeats(
    models,
    makers.slice(0, ROUNDTABLE_DEFAULT).map((mk) => ({ key: mk.family, family: mk.family, ref: mk.models[0].ref, name: "", edited: false })),
  );
}

/** Creates the members (follow-model avatars, one model each) and returns
 *  their ids in seat order; on a failure the ones made are removed. */
export async function createSeats(models: ModelsResponse | null, seats: Seat[], folder?: string, progress?: (n: number) => void): Promise<string[]> {
  const ids: string[] = [];
  try {
    for (const [i, s] of seats.entries()) {
      progress?.(i + 1);
      const a = await API.createAgent({
        name: s.name.trim(),
        role: t("rt.role", { model: shortModel(models, s.ref) }),
        instructions: t("rt.instr"),
        mode: teamMode("sandbox"),
        // a character too, for when the person switches the style back
        color: COLORS[(i * 5 + 2) % COLORS.length],
        shape: SHAPES[(i * 7 + 1) % SHAPES.length],
        face: "eyes",
        avatar: "vendor",
        folder,
        team: currentTeam(),
        modelRefs: [refString(s.ref)],
        // names the id when the name is not ASCII (通义千问 → qwen)
        template: s.family,
      });
      ids.push(a.id);
    }
  } catch (e) {
    await Promise.allSettled(ids.map((id) => API.deleteAgent(id)));
    throw e;
  }
  return ids;
}

/** The seat list in the 新建频道 dialog. */
export function RoundtablePicker({ models, makers, seats, setSeats }: { models: ModelsResponse | null; makers: Maker[]; seats: Seat[]; setSeats: (s: Seat[]) => void }) {
  const set = (next: Seat[]) => setSeats(nameSeats(models, next));
  const full = seats.length >= ROUNDTABLE_MAX;
  const rows = useMemo(() => makers.map((mk) => ({ mk, mine: seats.filter((s) => s.family === mk.family) })), [makers, seats]);
  if (makers.length === 0) return <div className="mpick rtpick"><div className="small rtnone">{t("rt.none")}</div></div>;
  const add = (mk: Maker) => {
    if (full) return;
    const used = seats.filter((s) => s.family === mk.family).map((s) => s.ref);
    const ref = (mk.models.find((m) => !used.some((u) => sameRef(u, m.ref))) ?? mk.models[0]).ref;
    set([...seats, { key: `${mk.family}-${Date.now()}`, family: mk.family, ref, name: "", edited: false }]);
  };
  return (
    <div className="mpick rtpick" role="listbox" aria-multiselectable="true" aria-label={t("rt.models")}>
      {rows.map(({ mk, mine }) =>
        mine.length === 0 ? (
          <button key={mk.family} type="button" role="option" aria-selected={false} className="mopt rtrow" disabled={full} onClick={() => add(mk)}>
            <span className="mbox" aria-hidden />
            <VendorTile icon={mk.mark} size={26} />
            <span className="g">
              <b>{productName(mk.family, mk.mark)}</b>
              <span>{t("rt.modelsN", { model: shortModel(models, mk.models[0].ref), n: mk.models.length })}</span>
            </span>
          </button>
        ) : (
          mine.map((s, j) => {
            const i = seats.indexOf(s);
            const upd = (p: Partial<Seat>) => set(seats.map((x) => (x === s ? { ...x, ...p } : x)));
            return (
              <div key={s.key} role="option" aria-selected className="mopt rtrow on">
                <button type="button" className="mbox on" aria-label={t("rt.removeSeat")} title={t("rt.removeSeat")} onClick={() => set(seats.filter((x) => x !== s))}>
                  {i + 1}
                </button>
                <VendorTile icon={modelMark(s.ref)} size={26} />
                <span className="g rtg">
                  <input
                    className="rtname"
                    value={s.name}
                    maxLength={24}
                    aria-label={t("rt.namePh")}
                    placeholder={t("rt.namePh")}
                    onChange={(e) => upd({ name: e.target.value, edited: e.target.value.trim() !== "" })}
                  />
                  {mk.models.length > 1 ? (
                    <select
                      className="rtmodel"
                      aria-label={t("na.model")}
                      value={refString(s.ref)}
                      onChange={(e) => {
                        const m = mk.models.find((x) => refString(x.ref) === e.target.value);
                        if (m) upd({ ref: m.ref });
                      }}
                    >
                      {mk.models.map((m) => (
                        <option key={refString(m.ref)} value={refString(m.ref)}>
                          {m.label}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <span className="rtmodel1">{mk.models[0].label}</span>
                  )}
                </span>
                {i === 0 ? (
                  <span className="pill blue">{t("ch.owner")}</span>
                ) : (
                  <button type="button" className="pill lead" title={t("nc.makeOwner")} onClick={() => set([s, ...seats.filter((x) => x !== s)])}>
                    <span className="pl1">{t("nc.member")}</span>
                    <span className="pl2">{t("nc.makeOwner")}</span>
                  </button>
                )}
                {j === mine.length - 1 && mk.models.length > mine.length ? (
                  <button type="button" className="ix rtadd" disabled={full} title={t("rt.addSeat", { vendor: productName(mk.family, mk.mark) })} aria-label={t("rt.addSeat", { vendor: productName(mk.family, mk.mark) })} onClick={() => add(mk)}>
                    <Plus size={14} />
                  </button>
                ) : (
                  <span className="rtadd" aria-hidden />
                )}
              </div>
            );
          })
        ),
      )}
      {full && <div className="small muted rtfull">{t("rt.max", { n: ROUNDTABLE_MAX })}</div>}
    </div>
  );
}

