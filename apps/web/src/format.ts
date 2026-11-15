import type { AgentView, ModelInfo, ModelRef, ModelsResponse, ProviderView } from "./api";
import { t, type Key } from "./i18n";

export function modeLabel(m: string | undefined): string {
  if (m === "read-only" || m === "sandbox" || m === "host") return t(`mode.${m}` as Key);
  return t("mode.sandbox");
}

export function statusLabel(s: string): string {
  return t(`status.${s}` as Key) || s;
}

export function statusTone(s: string): "run" | "wait" | "err" | "idle" | "ok" {
  switch (s) {
    case "running":
      return "run";
    case "waiting":
      return "wait";
    case "failed":
      return "err";
    default:
      return "idle";
  }
}

/** Human text for an agent's status detail ("takeover:<reason>", "approval:<id>"). */
/** A failure because no model is set up (wording from core/llm). */
export const NO_MODEL = /provider not configured|no (usable |available )?(model|provider)s?\b|unknown provider/i;

export function detailLabel(detail?: string): string {
  if (!detail) return "";
  if (NO_MODEL.test(detail)) return t("conn.none");
  if (detail.startsWith("takeover:")) return t("status.takeover", { reason: detail.slice(9) || "login" });
  if (detail.startsWith("approval:")) return t("list.waitApprove");
  return detail;
}

export function pillTone(s: string): string {
  return { running: "blue", waiting: "amber", failed: "red" }[s] ?? "";
}

export function effortLabel(e?: string): string {
  if (e === "low" || e === "medium" || e === "high") return t(`effort.${e}` as Key);
  return "";
}

export function hhmm(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
}

export function dayLabel(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const same = (a: Date, b: Date) => a.toDateString() === b.toDateString();
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (same(d, now)) return `${t("time.today")} ${hhmm(iso)}`;
  if (same(d, y)) return `${t("time.yesterday")} ${hhmm(iso)}`;
  return t("time.date", { m: d.getMonth() + 1, d: d.getDate(), time: hhmm(iso) });
}

export function ago(iso: string, now = Date.now()): string {
  const s = Math.max(0, (now - new Date(iso).getTime()) / 1000);
  if (s < 60) return t("time.justNow");
  if (s < 3600) return t("time.minutes", { n: Math.floor(s / 60) });
  if (s < 86400) return t("time.hours", { n: Math.floor(s / 3600) });
  return t("time.days", { n: Math.floor(s / 86400) });
}

export function until(iso: string): string {
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  if (s <= 0) return t("time.justNow");
  if (s < 3600) return t("time.minutes", { n: Math.ceil(s / 60) });
  if (s < 86400) return t("time.hours", { n: Math.round(s / 3600) });
  return t("time.days", { n: Math.round(s / 86400) });
}

export function kfmt(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1).replace(/\.0$/, "") + "M";
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1).replace(/\.0$/, "") + "K";
  return String(n);
}

// ---------------------------------------------------------------- models

export function findModel(models: ModelsResponse | null, r: ModelRef): ModelInfo | undefined {
  const p = models?.providers.find((x) => x.id === r.provider);
  return p?.models.find((m) => m.id === r.model);
}

export function findProvider(models: ModelsResponse | null, id: string): ProviderView | undefined {
  return models?.providers.find((x) => x.id === id);
}

const PROVIDER_NAMES: Record<string, string> = {
  anthropic: "Anthropic",
  openai: "OpenAI",
  gemini: "Gemini",
  deepseek: "DeepSeek",
  ollama: "Ollama",
  lmstudio: "LM Studio",
  "acp-claude-code": "Claude Code",
  "claude-code": "Claude Code",
  codex: "Codex",
  "acp-codex": "Codex",
  "gemini-cli": "Gemini CLI",
  "acp-gemini": "Gemini CLI",
  grok: "Grok",
  qwen: "Qwen",
  kimi: "Kimi",
  moonshot: "Moonshot",
  openrouter: "OpenRouter",
  mock: "Mock",
};

// Providers added from the UI: their chosen name and icon (GET /api/providers).
const USER_PROV: Record<string, { icon?: string; name?: string }> = {};

export function setUserProviders(list: { id: string; icon?: string; name?: string }[]) {
  for (const k of Object.keys(USER_PROV)) delete USER_PROV[k];
  for (const p of list) USER_PROV[p.id] = { icon: p.icon, name: p.name };
}

export function userProvider(id: string): { icon?: string; name?: string } | undefined {
  return USER_PROV[id];
}

export function providerName(id: string): string {
  return USER_PROV[id]?.name || PROVIDER_NAMES[id] || id.replace(/^acp-/, "").replace(/(^|-)(\w)/g, (_, a, b) => (a ? " " : "") + b.toUpperCase());
}

export function modelLabel(models: ModelsResponse | null, r: ModelRef): string {
  const m = findModel(models, r);
  if (m?.name) return m.name;
  return `${providerName(r.provider)} · ${r.model}`;
}

export function isExhausted(models: ModelsResponse | null, r: ModelRef): { until: string; reason: string } | null {
  const key = `${r.provider}/${r.model}`;
  const st = models?.exhausted?.find((x) => x.model === key || x.model === r.provider);
  if (st) return { until: st.until, reason: st.reason };
  const p = findProvider(models, r.provider);
  if (p?.quota?.known && p.quota.remaining <= 0) return { until: p.quota.resetsAt ?? "", reason: "quota_exhausted" };
  return null;
}

/** The model the agent would use next (first non-exhausted in its order). */
export function activeModel(models: ModelsResponse | null, a: AgentView | undefined): ModelRef | null {
  const order = a?.models ?? [];
  for (const r of order) if (!isExhausted(models, r)) return r;
  return order[0] ?? null;
}

/** All enabled models from every provider as refs. */
export function allModels(models: ModelsResponse | null): { ref: ModelRef; info: ModelInfo; provider: ProviderView }[] {
  const out: { ref: ModelRef; info: ModelInfo; provider: ProviderView }[] = [];
  for (const p of models?.providers ?? []) {
    for (const m of p.models ?? []) {
      if (m.enabled === false) continue;
      out.push({ ref: { provider: p.id, model: m.id }, info: m, provider: p });
    }
  }
  return out;
}

export function sameRef(a: ModelRef | null | undefined, b: ModelRef | null | undefined): boolean {
  return !!a && !!b && a.provider === b.provider && a.model === b.model;
}

export function clamp(n: number, lo: number, hi: number) {
  return Math.max(lo, Math.min(hi, n));
}

/** Compact time for list rows: 14:05 today, 昨天, then M/D. */
export function listTime(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const now = new Date();
  if (d.toDateString() === now.toDateString()) return hhmm(iso);
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (d.toDateString() === y.toDateString()) return t("time.yesterday");
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

/** "3 小时 12 分" style duration since iso. */
export function sinceLabel(iso: string, now = Date.now()): string {
  const m = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 60000));
  if (m < 60) return t("dur.m", { n: Math.max(1, m) });
  if (m < 1440) return m % 60 ? t("dur.hm", { h: Math.floor(m / 60), m: m % 60 }) : t("dur.h", { n: m / 60 });
  return t("dur.d", { n: Math.floor(m / 1440) });
}

/** Model display name without the provider / gateway suffix, e.g.
 *  "GPT-6.1 Sol · AlchemyPay-OpenAI" → "GPT-6.1 Sol". */
export function shortModel(models: ModelsResponse | null, r: ModelRef): string {
  const m = findModel(models, r);
  const name = m?.name || r.model.split("/").pop() || r.model;
  return tidyModelName(name.split(" · ")[0].replace(/\s*\((?:via|through)[^)]*\)\s*$/i, "").trim());
}

/** Friendly model names: "DeepSeek-V4.1-Flash" → "DeepSeek Flash",
 *  "deepseek-flash" → "DeepSeek Flash", "claude-opus-5-5" → "Claude Opus 5.5". */
export function tidyModelName(n: string): string {
  const ds = /^deepseek[-\s]?(?:v[\d.]+[-\s])?(flash|pro|chat|reasoner)(?:[-\s].*)?$/i.exec(n);
  if (ds) return "DeepSeek " + ds[1][0].toUpperCase() + ds[1].slice(1).toLowerCase();
  const cl = /^claude-(opus|sonnet|haiku|fable|mythos)-(\d+)(?:-(\d+))?$/i.exec(n);
  if (cl) return `Claude ${cl[1][0].toUpperCase()}${cl[1].slice(1)} ${cl[2]}${cl[3] ? "." + cl[3] : ""}`;
  if (/^[a-z0-9]+(?:-[a-z0-9.]+)+$/.test(n) && !/\d{4}/.test(n)) {
    // plain ids like "gpt-6.1-sol" → "GPT-6.1 Sol"
    const parts = n.split("-");
    const head = /^gpt|^o\d/.test(parts[0]) ? parts[0].toUpperCase() + (parts[1] && /^[\d.]+$/.test(parts[1]) ? "-" + parts.splice(1, 1)[0] : "") : parts[0][0].toUpperCase() + parts[0].slice(1);
    return [head, ...parts.slice(1).map((p) => (/^\d/.test(p) ? p : p[0].toUpperCase() + p.slice(1)))].join(" ");
  }
  return n;
}

/** Short label plus a tiny effort suffix when not the default/medium. */
export function shortModelEffort(models: ModelsResponse | null, r: ModelRef): { name: string; effort: string } {
  return { name: shortModel(models, r), effort: r.effort && r.effort !== "medium" ? effortLabel(r.effort) : "" };
}

/** Next 08:00 local time after now. */
export function nextMorning(): Date {
  const d = new Date();
  d.setDate(d.getDate() + (d.getHours() >= 8 ? 1 : 0));
  d.setHours(8, 0, 0, 0);
  return d;
}
