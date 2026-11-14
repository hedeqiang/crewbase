import type { CSSProperties } from "react";
import { providerName } from "../format";

// Simple provider glyphs (coloured rounded square + initial). @lobehub/icons
// needs React 19 + @lobehub/ui, so it is not used here.
const COLORS: Record<string, string> = {
  anthropic: "#d97757",
  "acp-claude-code": "#d97757",
  "claude-code": "#d97757",
  openai: "#10a37f",
  codex: "#141414",
  "acp-codex": "#141414",
  gemini: "#4285f4",
  "gemini-cli": "#4285f4",
  "acp-gemini": "#4285f4",
  deepseek: "#4d6bfe",
  ollama: "#6b6b70",
  lmstudio: "#5b5bd6",
  grok: "#141414",
  qwen: "#615ced",
  kimi: "#1f1f1f",
  moonshot: "#1f1f1f",
  openrouter: "#6467f2",
  mock: "#9b9ba3",
};

export function ProviderMark({ provider, size = 14 }: { provider: string; size?: number }) {
  const name = providerName(provider);
  const bg = COLORS[provider] ?? "#8a8a90";
  return (
    <span
      className="pmark"
      title={name}
      aria-hidden
      style={{ "--pm": bg, width: size, height: size, fontSize: Math.max(8, size * 0.62) } as CSSProperties}
    >
      {name.charAt(0).toUpperCase()}
    </span>
  );
}
