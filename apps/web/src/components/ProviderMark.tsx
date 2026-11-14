import type { CSSProperties } from "react";
import { providerName } from "../format";

// Model vendor icons from LobeHub Icons (@lobehub/icons-static-svg, MIT,
// https://github.com/lobehub/lobe-icons). Imported one by one as raw SVG so
// only these are bundled and monochrome icons follow `currentColor`.
// Crewbase's own mark and agent avatars are original (see Logo.tsx).
import anthropic from "@lobehub/icons-static-svg/icons/anthropic.svg?raw";
import claude from "@lobehub/icons-static-svg/icons/claude-color.svg?raw";
import codex from "@lobehub/icons-static-svg/icons/codex-color.svg?raw";
import cursor from "@lobehub/icons-static-svg/icons/cursor.svg?raw";
import deepseek from "@lobehub/icons-static-svg/icons/deepseek-color.svg?raw";
import doubao from "@lobehub/icons-static-svg/icons/doubao-color.svg?raw";
import gemini from "@lobehub/icons-static-svg/icons/gemini-color.svg?raw";
import grok from "@lobehub/icons-static-svg/icons/grok.svg?raw";
import groq from "@lobehub/icons-static-svg/icons/groq.svg?raw";
import kimi from "@lobehub/icons-static-svg/icons/kimi-color.svg?raw";
import lmstudio from "@lobehub/icons-static-svg/icons/lmstudio.svg?raw";
import magpie from "@lobehub/icons-static-svg/icons/magpie.svg?raw";
import minimax from "@lobehub/icons-static-svg/icons/minimax-color.svg?raw";
import mistral from "@lobehub/icons-static-svg/icons/mistral-color.svg?raw";
import moonshot from "@lobehub/icons-static-svg/icons/moonshot.svg?raw";
import ollama from "@lobehub/icons-static-svg/icons/ollama.svg?raw";
import openai from "@lobehub/icons-static-svg/icons/openai.svg?raw";
import opencode from "@lobehub/icons-static-svg/icons/opencode.svg?raw";
import openrouter from "@lobehub/icons-static-svg/icons/openrouter-color.svg?raw";
import qwen from "@lobehub/icons-static-svg/icons/qwen-color.svg?raw";
import siliconcloud from "@lobehub/icons-static-svg/icons/siliconcloud-color.svg?raw";
import vllm from "@lobehub/icons-static-svg/icons/vllm-color.svg?raw";
import zhipu from "@lobehub/icons-static-svg/icons/zhipu-color.svg?raw";

const ICONS: Record<string, string> = {
  anthropic, claude, codex, cursor, deepseek, doubao, gemini, grok, groq, kimi, lmstudio, magpie,
  minimax, mistral, moonshot, ollama, openai, opencode, openrouter, qwen, siliconcloud, vllm, zhipu,
};

// Provider id → icon key. ACP providers are "acp-<cli>".
const BY_PROVIDER: Record<string, string> = {
  anthropic: "anthropic",
  "acp-claude-code": "claude",
  "claude-code": "claude",
  claude: "claude",
  openai: "openai",
  "acp-codex": "codex",
  codex: "codex",
  gemini: "gemini",
  google: "gemini",
  "acp-gemini-cli": "gemini",
  "gemini-cli": "gemini",
  deepseek: "deepseek",
  ollama: "ollama",
  lmstudio: "lmstudio",
  vllm: "vllm",
  xai: "grok",
  grok: "grok",
  "acp-grok": "grok",
  qwen: "qwen",
  moonshot: "moonshot",
  kimi: "kimi",
  openrouter: "openrouter",
  "acp-cursor": "cursor",
  cursor: "cursor",
  "acp-opencode": "opencode",
  opencode: "opencode",
  mistral: "mistral",
  groq: "groq",
  zhipu: "zhipu",
  siliconflow: "siliconcloud",
  doubao: "doubao",
  minimax: "minimax",
  magpie: "magpie",
};

// Gateways (magpie, OpenRouter) serve many vendors: recognise the vendor
// from the model id ("deepseek/deepseek-flash", "auto/claude-opus-5-5").
const BY_MODEL: [RegExp, string][] = [
  [/claude/i, "claude"],
  [/(^|\/)(gpt|o\d|chatgpt)/i, "openai"],
  [/codex/i, "codex"],
  [/gemini/i, "gemini"],
  [/deepseek/i, "deepseek"],
  [/qwen|qwq/i, "qwen"],
  [/kimi|moonshot/i, "kimi"],
  [/grok/i, "grok"],
  [/glm|zhipu/i, "zhipu"],
  [/mistral|codestral/i, "mistral"],
  [/doubao/i, "doubao"],
  [/minimax/i, "minimax"],
  [/composer|cursor/i, "cursor"],
];

const GATEWAYS = new Set(["magpie", "openrouter"]);

/** Icon key for a provider (and, behind a gateway, its model). */
export function vendorIcon(provider: string, model?: string): string | undefined {
  if (model && GATEWAYS.has(provider)) {
    const first = model.split("/")[0].toLowerCase();
    if (BY_PROVIDER[first] && first !== provider) return BY_PROVIDER[first];
    for (const [re, key] of BY_MODEL) if (re.test(model)) return key;
  }
  return BY_PROVIDER[provider];
}

export function ProviderMark({ provider, model, size = 14 }: { provider: string; model?: string; size?: number }) {
  const name = providerName(provider);
  const key = vendorIcon(provider, model);
  const svg = key ? ICONS[key] : undefined;
  if (svg) {
    return (
      <span
        className="pmark icon"
        title={name}
        aria-hidden
        style={{ width: size, height: size, fontSize: size } as CSSProperties}
        dangerouslySetInnerHTML={{ __html: svg }}
      />
    );
  }
  // Unknown vendor: neutral monogram (never an imitation of a brand).
  return (
    <span className="pmark" title={name} aria-hidden style={{ width: size, height: size, fontSize: Math.max(8, size * 0.6) } as CSSProperties}>
      {name.charAt(0).toUpperCase()}
    </span>
  );
}
