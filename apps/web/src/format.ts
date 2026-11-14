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
  return `${d.getMonth() + 1}月${d.getDate()}日 ${hhmm(iso)}`;
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

export function providerName(id: string): string {
  return PROVIDER_NAMES[id] ?? id.replace(/^acp-/, "").replace(/(^|-)(\w)/g, (_, a, b) => (a ? " " : "") + b.toUpperCase());
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
