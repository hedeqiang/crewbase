// What one conversation used, from its own log: every model call of every
// turn (the reply's usage events), summed, priced at the models' list
// prices, by model and by turn. Nothing is asked of the daemon.

import type { ModelRef, ModelsResponse, SessionEvent } from "./api";
import { billingOf, findModel, priceCost } from "./format";
import type { Usage } from "./projection";

export interface UsageRow {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  calls: number;
  /** USD of the calls whose model has a price; see unpriced */
  cost: number;
  /** calls that ran on a model with no known price (not local) */
  unpriced: number;
  /** calls that ran on a local model (free) */
  local: number;
  /** model time of the calls that have one, and the output they wrote */
  ms: number;
  timedOut: number;
}

export interface ModelUsage extends UsageRow {
  model: ModelRef;
}

export interface TurnUsage extends UsageRow {
  /** the person's message that started it (a line of it) */
  prompt: string;
  time: string;
  agent: string;
}

export interface SessionUsage {
  total: UsageRow;
  byModel: ModelUsage[];
  /** newest first */
  turns: TurnUsage[];
}

const zero = (): UsageRow => ({ input: 0, output: 0, cacheRead: 0, cacheWrite: 0, calls: 0, cost: 0, unpriced: 0, local: 0, ms: 0, timedOut: 0 });

function add(r: UsageRow, u: Usage, cost: number | null, local: boolean, ms?: number) {
  r.input += u.input;
  r.output += u.output;
  r.cacheRead += u.cacheRead ?? 0;
  r.cacheWrite += u.cacheWrite ?? 0;
  r.calls++;
  if (cost !== null) r.cost += cost;
  else if (local) r.local++;
  else r.unpriced++;
  if (ms !== undefined) {
    r.ms += ms;
    r.timedOut += u.output;
  }
}

const refKey = (r: ModelRef) => r.provider + "/" + r.model;

/** The conversation's usage, from its events (optionally one agent's). */
export function sessionUsage(events: SessionEvent[], models: ModelsResponse | null, agent?: string): SessionUsage {
  const total = zero();
  const byModel = new Map<string, ModelUsage>();
  const turns: TurnUsage[] = [];
  const open = new Map<string, TurnUsage>();
  let prompt = "";
  let stepAt = new Map<string, number>();
  for (const e of events) {
    if (agent && e.agent && e.agent !== agent && e.type !== "message/user") continue;
    const d: any = e.data ?? {};
    switch (e.type) {
      case "message/user":
        if (typeof d.text === "string" && d.text.trim()) prompt = d.text.trim().split("\n")[0].slice(0, 120);
        break;
      case "turn/start": {
        const tu: TurnUsage = { ...zero(), prompt, time: e.time, agent: e.agent ?? "" };
        open.set(e.agent ?? "", tu);
        turns.push(tu);
        break;
      }
      case "step/start":
        stepAt.set(e.agent ?? "", new Date(e.time).getTime());
        break;
      case "message/assistant": {
        const u = d.usage ?? {};
        const usage: Usage = { input: u.input ?? 0, output: u.output ?? 0, cacheRead: u.cacheRead, cacheWrite: u.cacheWrite };
        if (!usage.input && !usage.output) break;
        const ref: ModelRef | undefined = d.model?.provider ? d.model : undefined;
        const b = billingOf(models, ref);
        const price = b === "api" && ref ? findModel(models, ref)?.pricing : undefined;
        const cost = price ? priceCost(price, usage) : null;
        const at = stepAt.get(e.agent ?? "");
        const ts = new Date(e.time).getTime();
        const ms = at !== undefined && ts >= at ? ts - at : undefined;
        stepAt.delete(e.agent ?? "");
        add(total, usage, cost, b === "local", ms);
        if (ref) {
          const k = refKey(ref);
          let m = byModel.get(k);
          if (!m) byModel.set(k, (m = { ...zero(), model: ref }));
          add(m, usage, cost, b === "local", ms);
        }
        const tu = open.get(e.agent ?? "");
        if (tu) add(tu, usage, cost, b === "local", ms);
        break;
      }
      case "turn/end":
        open.delete(e.agent ?? "");
        break;
    }
  }
  return {
    total,
    byModel: [...byModel.values()].sort((a, b) => b.input + b.output - (a.input + a.output)),
    turns: turns.filter((x) => x.calls > 0).reverse(),
  };
}

/** Share of input tokens read from the prompt cache, 0..1; null without input. */
export function cacheHit(r: UsageRow): number | null {
  return r.input > 0 ? r.cacheRead / r.input : null;
}

/** Output tokens per second of model time; null when too short to say. */
export function speedOf(r: UsageRow): number | null {
  return r.ms >= 500 && r.timedOut >= 20 ? r.timedOut / (r.ms / 1000) : null;
}
