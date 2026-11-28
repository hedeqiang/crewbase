// 插件与技能: the catalog API and the shapes and helpers the pages share.

import { type ReactNode } from "react";
import { Bug, CalendarDays, GitCommitHorizontal, Globe, ListChecks, Megaphone, Plug, SearchCode, Sparkles, Table } from "lucide-react";
import { api, type MarketEntry, type McpServer, type PluginView } from "../../../api";
import { getLang } from "../../../i18n";
import { MonoMark } from "../../ProviderMark";

// Brand icons from LobeHub Icons (@lobehub/icons-static-svg, MIT), as in
// ProviderMark, where the package has the mark; the others are the
// companies' own marks from their brand pages or sites (src/assets/brands).
// Anything else gets a letter tile.
import github from "@lobehub/icons-static-svg/icons/github.svg?raw";
import notion from "@lobehub/icons-static-svg/icons/notion.svg?raw";
import cloudflare from "@lobehub/icons-static-svg/icons/cloudflare-color.svg?raw";
import huggingface from "@lobehub/icons-static-svg/icons/huggingface-color.svg?raw";
import devin from "@lobehub/icons-static-svg/icons/devin-color.svg?raw";
import exa from "@lobehub/icons-static-svg/icons/exa-color.svg?raw";
import mcp from "@lobehub/icons-static-svg/icons/mcp.svg?raw";
import context7 from "../../../assets/brands/context7.svg?raw";
import sentry from "../../../assets/brands/sentry.svg?raw";
import linear from "../../../assets/brands/linear.svg?raw";
import stripe from "../../../assets/brands/stripe.svg?raw";
import atlassian from "../../../assets/brands/atlassian.svg?raw";
import canva from "../../../assets/brands/canva.svg?raw";
import chrome from "../../../assets/brands/chrome.svg?raw";
import supabase from "../../../assets/brands/supabase.svg?raw";
import firecrawl from "@lobehub/icons-static-svg/icons/firecrawl-color.svg?raw";

/** A mark and how it sits in the 40px tile: "tile" = the mark is its own
 *  rounded square and fills the tile; "wide" = a wordmark. */
export const BRAND: Record<string, { svg: string; fit?: "tile" | "wide" }> = {
  github: { svg: github }, notion: { svg: notion }, cloudflare: { svg: cloudflare },
  huggingface: { svg: huggingface }, devin: { svg: devin }, exa: { svg: exa }, mcp: { svg: mcp },
  context7: { svg: context7, fit: "tile" }, stripe: { svg: stripe, fit: "tile" }, sentry: { svg: sentry },
  linear: { svg: linear }, atlassian: { svg: atlassian }, canva: { svg: canva, fit: "wide" },
  chrome: { svg: chrome }, supabase: { svg: supabase }, firecrawl: { svg: firecrawl },
};
export const SKILL_ICONS: Record<string, ReactNode> = {
  "search-code": <SearchCode size={19} />, "git-commit": <GitCommitHorizontal size={19} />, bug: <Bug size={19} />,
  megaphone: <Megaphone size={19} />, globe: <Globe size={19} />, table: <Table size={19} />,
  "list-checks": <ListChecks size={19} />, "calendar-days": <CalendarDays size={19} />,
};

// ---------------------------------------------------------------- catalog API

export type Text = { zh: string; en: string };
export interface CatInput { id: string; label: Text; help?: Text; placeholder?: string; secret?: boolean; optional?: boolean }
export interface CatItem {
  id: string;
  kind: "mcp" | "skill";
  category: string;
  name: string;
  icon?: string;
  desc: Text;
  /** device = a built-in sign-in (signIn) runs; inputs are only its fallback. */
  auth: "none" | "oauth" | "token" | "setup" | "device";
  signIn?: string;
  docs?: string;
  setup?: Text;
  inputs?: CatInput[];
  transport?: string;
  endpoint?: string;
  installed: boolean;
  server?: string;
  scope?: string;
}
export interface CatCategory { id: string; title: Text; desc: Text }
export interface SkillLibrary { id: string; name: string; repo: string; author: string; desc: Text; picks?: string[] }
export interface Catalog { categories: CatCategory[]; items: CatItem[]; libraries?: SkillLibrary[] }
export interface SkillView { name: string; description: string; dir: string; source: string; agent?: string; version?: string }
/** A built-in device sign-in (GitHub). */
export interface SignInView {
  state: "pending" | "done" | "denied" | "expired" | "error";
  userCode?: string;
  verificationUri?: string;
  expiresAt?: string;
  error?: string;
  server?: string;
  noInstallation?: boolean;
  installUrl?: string;
}

export const enc = encodeURIComponent;
export const catalogAPI = {
  list: () => api<Catalog>("GET", "/api/catalog"),
  install: (id: string, b: { inputs?: Record<string, string> } & Target) => api<{ kind: string; server?: McpServer }>("POST", `/api/catalog/${enc(id)}/install`, b),
  uninstall: (id: string) => api("DELETE", `/api/catalog/${enc(id)}`),
  signIn: (id: string, b: Target = {}) => api<SignInView>("POST", `/api/catalog/${enc(id)}/signin`, b),
  signInState: (id: string) => api<SignInView>("GET", `/api/catalog/${enc(id)}/signin`),
  cancelSignIn: (id: string) => api("DELETE", `/api/catalog/${enc(id)}/signin`),
  skills: () => api<{ skills: SkillView[] }>("GET", "/api/skills"),
  removeSkill: (name: string, agent?: string) => api("DELETE", `/api/skills/${enc(name)}${agent ? "?agent=" + enc(agent) : ""}`),
};

/** Who a new connection is for: an explicit scope ("global") or the
 *  context it is made from (a team: the server scopes it to that team). */
export type Target = { scope?: string; context?: string };

/** A global connector with at least this many tools gets a notice
 *  suggesting to give it only to the agents that need it. */
export const MANY_TOOLS = 20;

export const tx = (x?: Text) => (x ? (getLang() === "en" ? x.en : x.zh) || x.en || x.zh : "");
export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** How clicking 连接 proceeds for an item. A new kind of catalog item (e.g.
 *  a built-in connector) adds a case here and in MarketView.connect. */
export type ConnectHow = "direct" | "oauth" | "device" | "dialog";
export function connectHow(it: CatItem): ConnectHow {
  if (it.kind === "skill") return "direct";
  switch (it.auth) {
    case "device":
      return "device";
    case "oauth":
      return "oauth";
    case "none":
      return (it.inputs ?? []).some((x) => !x.optional) ? "dialog" : "direct";
    default: // token, setup: something must be pasted or done first
      return "dialog";
  }
}

/** The server answers 401/403: its token expired or was revoked. */
export const needsReauth = (s?: McpServer) => s?.status?.state === "error" && /\b(401|403)\b|unauthori[sz]ed|forbidden|bad credentials/i.test(s.status.error ?? "");

// ---------------------------------------------------------------- page

export type View = "market" | "installed";

export interface Data {
  catalog: Catalog | null;
  servers: McpServer[] | null;
  skills: SkillView[] | null;
  plugins: PluginView[] | null;
  market: MarketEntry[] | null;
  marketErr: string | null;
}



export interface Shared {
  onPaste: (text: string, picks?: string[]) => void;
  /** the search box, placed by the view */
  search: ReactNode;
  d: Data;
  q: string;
  reload: () => void;
  reloadMarket: () => Promise<void>;
  onApprove: (p: PluginView) => void;
  /** Opens the built-in sign-in (GitHub device flow) for an item. */
  onSignIn: (it: CatItem) => void;
  onScope: (s: McpServer) => void;
  /** Opens the per-agent checklist for a connector. */
  onAccess: (s: McpServer) => void;
  /** Where new connections go (Target). */
  target: Target;
  toAll: boolean;
  setToAll: (v: boolean) => void;
  onManage: (server: string) => void;
  focus: string | null;
}

/** v1.2.3 (a version that already starts with v or is not a number as is). */
export const verLabel = (v: string) => (/^\d/.test(v.trim()) ? "v" + v.trim() : v.trim());

/** The MCP servers features/agentdocs makes (one per agent): shown as the
 *  Open Agent Docs app, not as connectors. */
export const isDocsServer = (name: string) => /^docs-[0-9a-f]{8}$/.test(name);

export function matches(q: string, ...xs: (string | undefined)[]) {
  return !q || xs.some((x) => x?.toLowerCase().includes(q));
}

/** The 40px tile in front of a row. */
export function ItemIcon({ icon, id, label, kind }: { icon?: string; id: string; label: string; kind?: string }) {
  const b = icon ? BRAND[icon] : undefined;
  if (b) return <span className={"mkic brand" + (b.fit ? " " + b.fit : "")} aria-hidden dangerouslySetInnerHTML={{ __html: b.svg }} />;
  if (kind === "skill") return <span className="mkic">{(icon && SKILL_ICONS[icon]) || <Sparkles size={19} />}</span>;
  if (kind === "plugin") return <span className="mkic"><Plug size={19} /></span>;
  return <MonoMark id={id} label={label} size={40} />;
}
