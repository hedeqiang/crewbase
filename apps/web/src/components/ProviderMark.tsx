import type { CSSProperties } from "react";
import { providerName, userProvider } from "../format";

// Model vendor icons from LobeHub Icons (@lobehub/icons-static-svg, MIT,
// https://github.com/lobehub/lobe-icons). Imported one by one as raw SVG so
// only these are bundled and monochrome icons follow `currentColor`.
// Crewbase's own mark and agent avatars are original (see Logo.tsx).
import anthropic from "@lobehub/icons-static-svg/icons/anthropic.svg?raw";
import claude from "@lobehub/icons-static-svg/icons/claude-color.svg?raw";
import codex from "@lobehub/icons-static-svg/icons/codex-color.svg?raw";
import cursor from "@lobehub/icons-static-svg/icons/cursor.svg?raw";
import githubcopilot from "@lobehub/icons-static-svg/icons/githubcopilot.svg?raw";
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
import xai from "@lobehub/icons-static-svg/icons/xai.svg?raw";
import volcengine from "@lobehub/icons-static-svg/icons/volcengine-color.svg?raw";
import baichuan from "@lobehub/icons-static-svg/icons/baichuan-color.svg?raw";
import hunyuan from "@lobehub/icons-static-svg/icons/hunyuan-color.svg?raw";
import stepfun from "@lobehub/icons-static-svg/icons/stepfun-color.svg?raw";
import yi from "@lobehub/icons-static-svg/icons/yi-color.svg?raw";
import wenxin from "@lobehub/icons-static-svg/icons/wenxin-color.svg?raw";
import spark from "@lobehub/icons-static-svg/icons/spark-color.svg?raw";
import together from "@lobehub/icons-static-svg/icons/together-color.svg?raw";
import fireworks from "@lobehub/icons-static-svg/icons/fireworks-color.svg?raw";
import perplexity from "@lobehub/icons-static-svg/icons/perplexity-color.svg?raw";
import cohere from "@lobehub/icons-static-svg/icons/cohere-color.svg?raw";
import nvidia from "@lobehub/icons-static-svg/icons/nvidia-color.svg?raw";
import azure from "@lobehub/icons-static-svg/icons/azure-color.svg?raw";
import bedrock from "@lobehub/icons-static-svg/icons/bedrock-color.svg?raw";
import vertexai from "@lobehub/icons-static-svg/icons/vertexai-color.svg?raw";
import cloudflare from "@lobehub/icons-static-svg/icons/cloudflare-color.svg?raw";
import antigravity from "@lobehub/icons-static-svg/icons/antigravity-color.svg?raw";
import meta from "@lobehub/icons-static-svg/icons/meta-color.svg?raw";

const ICONS: Record<string, string> = {
  anthropic, claude, codex, cursor, deepseek, doubao, gemini, grok, groq, kimi, lmstudio, magpie,
  minimax, mistral, moonshot, ollama, openai, opencode, openrouter, qwen, siliconcloud, vllm, zhipu,
  xai, volcengine, baichuan, hunyuan, stepfun, yi, wenxin, spark, together, fireworks, perplexity,
  cohere, nvidia, azure, bedrock, vertexai, cloudflare, antigravity, meta, githubcopilot,
};

/** The raw SVG of an icon key (undefined when there is none). */
export function markSvg(key: string): string | undefined {
  return ICONS[key];
}

/** Vendor marks an agent can wear as its avatar, in groups: model makers
 *  abroad, model makers in China, then platforms, tools and local runners. */
export const AVATAR_VENDORS: { group: "intl" | "cn" | "platform"; keys: string[] }[] = [
  { group: "intl", keys: ["claude", "anthropic", "openai", "codex", "gemini", "grok", "xai", "meta", "mistral", "cohere", "perplexity"] },
  { group: "cn", keys: ["deepseek", "qwen", "kimi", "moonshot", "zhipu", "minimax", "doubao", "volcengine", "hunyuan", "wenxin", "baichuan", "stepfun", "yi", "spark"] },
  {
    group: "platform",
    keys: ["openrouter", "siliconcloud", "groq", "together", "fireworks", "nvidia", "azure", "bedrock", "vertexai", "cloudflare", "cursor", "opencode", "antigravity", "ollama", "lmstudio", "vllm"],
  },
];

const MARK_NAMES: Record<string, string> = {
  claude: "Claude", codex: "Codex", grok: "Grok", moonshot: "Moonshot", cursor: "Cursor", opencode: "OpenCode",
  antigravity: "Antigravity", meta: "Meta", magpie: "Magpie",
};

/** A mark's display name (the vendor or product it stands for). */
export function markName(key: string): string {
  return MARK_NAMES[key] ?? PICKABLE.find((p) => p.key === key)?.name ?? key;
}

// Marks of one maker: a fixed avatar of one stands for the others too.
const FAMILY: Record<string, string> = {
  claude: "anthropic", codex: "openai", grok: "xai", kimi: "moonshot", doubao: "bytedance", volcengine: "bytedance",
  vertexai: "google", gemini: "google", antigravity: "google",
};

/** The maker an icon key belongs to ("claude" and "anthropic" are one). */
export function vendorFamily(key: string): string {
  return FAMILY[key] ?? key;
}


/** Icons a person can pick for a custom provider (vendor icons, then "mono"). */
export const PICKABLE: { key: string; name: string }[] = [
  { key: "openai", name: "OpenAI" }, { key: "anthropic", name: "Anthropic" }, { key: "gemini", name: "Gemini" },
  { key: "deepseek", name: "DeepSeek" }, { key: "xai", name: "xAI" }, { key: "kimi", name: "Kimi" },
  { key: "zhipu", name: "智谱" }, { key: "minimax", name: "MiniMax" }, { key: "qwen", name: "通义千问" },
  { key: "volcengine", name: "火山引擎" }, { key: "doubao", name: "豆包" }, { key: "hunyuan", name: "混元" },
  { key: "wenxin", name: "文心" }, { key: "baichuan", name: "百川" }, { key: "stepfun", name: "阶跃星辰" },
  { key: "yi", name: "零一万物" }, { key: "spark", name: "讯飞星火" }, { key: "mistral", name: "Mistral" },
  { key: "openrouter", name: "OpenRouter" }, { key: "siliconcloud", name: "SiliconFlow" }, { key: "groq", name: "Groq" },
  { key: "together", name: "Together" }, { key: "fireworks", name: "Fireworks" }, { key: "perplexity", name: "Perplexity" },
  { key: "cohere", name: "Cohere" }, { key: "nvidia", name: "NVIDIA" }, { key: "azure", name: "Azure" },
  { key: "bedrock", name: "Bedrock" }, { key: "vertexai", name: "Vertex AI" }, { key: "cloudflare", name: "Cloudflare" },
  { key: "ollama", name: "Ollama" }, { key: "lmstudio", name: "LM Studio" }, { key: "vllm", name: "vLLM" },
];

// Where a custom endpoint lives says who runs it.
const BY_HOST: [RegExp, string][] = [
  [/anthropic\.com/, "anthropic"], [/openai\.com|openai\.azure/, "openai"], [/googleapis\.com|google/, "gemini"],
  [/deepseek/, "deepseek"], [/(^|\.)x\.ai$/, "xai"], [/moonshot|kimi/, "kimi"], [/bigmodel|zhipu|z\.ai$/, "zhipu"],
  [/minimax/, "minimax"], [/dashscope|aliyun|bailian/, "qwen"], [/volces|volcengine|ark\./, "volcengine"],
  [/openrouter/, "openrouter"], [/siliconflow/, "siliconcloud"], [/groq\.com/, "groq"], [/mistral\.ai/, "mistral"],
  [/together/, "together"], [/fireworks/, "fireworks"], [/perplexity/, "perplexity"], [/cohere/, "cohere"],
  [/nvidia/, "nvidia"], [/azure/, "azure"], [/bedrock|amazonaws/, "bedrock"], [/cloudflare/, "cloudflare"],
  [/baichuan/, "baichuan"], [/hunyuan|tencent/, "hunyuan"], [/stepfun/, "stepfun"], [/lingyiwanwu|01\.ai/, "yi"],
  [/baidu|qianfan|baidubce/, "wenxin"], [/xf-yun|xfyun|xunfei/, "spark"],
];

/** Picks an icon for a custom provider: from the endpoint host, the name,
 *  then the models it serves; "mono" (a letter tile) when unknown. */
export function autoIcon(baseURL: string, name = "", models: string[] = []): string {
  let host = "";
  try {
    host = new URL(baseURL).hostname.toLowerCase();
  } catch {
    /* not a URL yet */
  }
  if (host) {
    const port = (() => {
      try {
        return new URL(baseURL).port;
      } catch {
        return "";
      }
    })();
    if ((host === "127.0.0.1" || host === "localhost") && port === "11434") return "ollama";
    if ((host === "127.0.0.1" || host === "localhost") && port === "1234") return "lmstudio";
    for (const [re, key] of BY_HOST) if (re.test(host)) return key;
  }
  const n = name.toLowerCase();
  if (n) {
    for (const p of PICKABLE) if (n.includes(p.key) || n.includes(p.name.toLowerCase())) return p.key;
    for (const [re, key] of BY_HOST) if (re.test(n)) return key;
  }
  // the vendor most of its models come from (a gateway of one vendor)
  const votes: Record<string, number> = {};
  for (const m of models) {
    for (const [re, key] of BY_MODEL) {
      if (re.test(m)) {
        votes[key] = (votes[key] ?? 0) + 1;
        break;
      }
    }
  }
  const top = Object.entries(votes).sort((a, b) => b[1] - a[1])[0];
  if (top && top[1] * 2 > models.length) return top[0];
  return "mono";
}

/** Our own letter tile: the first letter on a soft tint picked from the
 *  provider id (stable across sessions, never a vendor's mark). */
export function MonoMark({ id, label, size = 14 }: { id: string; label: string; size?: number }) {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  const hue = [210, 160, 265, 25, 340, 190, 45, 120][h % 8];
  const ch = (label.trim() || id).replace(/^[^\p{L}\p{N}]+/u, "").charAt(0).toUpperCase() || "?";
  return (
    <span
      className="pmark mono-tile"
      title={label}
      aria-hidden
      style={{ width: size, height: size, fontSize: Math.max(8, size * 0.56), ["--h" as string]: hue } as CSSProperties}
    >
      {ch}
    </span>
  );
}

// Provider id → icon key.
const BY_PROVIDER: Record<string, string> = {
  anthropic: "anthropic",
  claude: "claude",
  openai: "openai",
  codex: "codex",
  gemini: "gemini",
  google: "gemini",
  antigravity: "antigravity",
  deepseek: "deepseek",
  ollama: "ollama",
  lmstudio: "lmstudio",
  vllm: "vllm",
  xai: "xai",
  grok: "grok",
  qwen: "qwen",
  moonshot: "moonshot",
  kimi: "kimi",
  openrouter: "openrouter",
  cursor: "cursor",
  copilot: "githubcopilot",
  githubcopilot: "githubcopilot",
  opencode: "opencode",
  mistral: "mistral",
  groq: "groq",
  zhipu: "zhipu",
  siliconflow: "siliconcloud",
  doubao: "doubao",
  minimax: "minimax",
  volcengine: "volcengine",
  ark: "volcengine",
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
  [/llama/i, "meta"],
];

const GATEWAYS = new Set(["magpie", "openrouter"]);

/** Icon key for a provider (and, behind a gateway, its model). */
export function vendorIcon(provider: string, model?: string): string | undefined {
  if (model && GATEWAYS.has(provider)) {
    const first = model.split("/")[0].toLowerCase();
    if (BY_PROVIDER[first] && first !== provider) return BY_PROVIDER[first];
    for (const [re, key] of BY_MODEL) if (re.test(model)) return key;
  }
  const u = userProvider(provider)?.icon;
  if (u) return u;
  return BY_PROVIDER[provider];
}

/** The mark of who made a model: its id says so first ("cursor/claude-…"
 *  is Claude, "qwen3" on Ollama is Qwen), else the provider's mark
 *  (vendorIcon). An agent's vendor avatar wears this one. */
export function modelMaker(provider: string, model?: string): string | undefined {
  const id = model?.split("/").pop() ?? "";
  if (id) for (const [re, key] of BY_MODEL) if (re.test(id)) return key;
  return vendorIcon(provider, model);
}

/** Renders an icon key directly (pickers, previews). */
export function IconMark({ icon, id, label, size = 14 }: { icon: string; id: string; label: string; size?: number }) {
  const svg = ICONS[icon];
  if (!svg) return <MonoMark id={id} label={label} size={size} />;
  return (
    <span
      className="pmark icon"
      title={label}
      aria-hidden
      style={{ width: size, height: size, fontSize: size } as CSSProperties}
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  );
}

export function ProviderMark({ provider, model, size = 14 }: { provider: string; model?: string; size?: number }) {
  const name = providerName(provider);
  const key = vendorIcon(provider, model);
  if (key === "mono") return <MonoMark id={provider} label={name} size={size} />;
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
