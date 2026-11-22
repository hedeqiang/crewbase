import { getLang } from "./i18n";
import { useEffect, useState } from "react";
import { apiFetch, tunnelBlobURL, viaRelay } from "./tunnel";
// REST helpers and API types (mirrors features/api, core/session, core/agent,
// capabilities/approval). The page is served by crewd on the same origin.

export type Mode = "read-only" | "sandbox" | "host";
export type Status = "idle" | "running" | "waiting" | "paused" | "failed";

export interface ModelRef {
  provider: string;
  model: string;
  effort?: string;
}

/** A file saved in an agent workspace (POST /api/agents/{id}/uploads). */
export interface Attachment {
  path: string;
  name: string;
  mime: string;
  size: number;
  sha256?: string;
}

export interface Input {
  id: string;
  session: string;
  text: string;
  from?: string;
  model?: ModelRef;
  front?: boolean;
  queued: string;
}

export interface AgentView {
  id: string;
  /** session a waiting status concerns (approval / takeover) */
  statusSession?: string;
  name: string;
  role?: string;
  instructions?: string;
  models: ModelRef[] | null;
  mode: Mode | "";
  modeUntil?: string;
  color?: string;
  shape?: string;
  face?: string;
  folder?: string;
  host?: string;
  created: string;
  session: string;
  status: Status;
  detail?: string;
  paused: boolean;
  queue: Input[];
  /** project folder the agent works in (unset = its own workspace) */
  sharedWorkspace?: string;
  team?: string;
  workspace: string;
  effectiveMode: Mode;
  nativeTools?: string[] | null;
  /** when deferred tools are held back: "" auto | "always" | "never" */
  toolSearch?: string;
  /** deferred tools or groups loaded from the start */
  alwaysLoad?: string[] | null;
  /** connectors (by name) the agent may use; unset/null = every connector its scope allows */
  connectors?: string[] | null;
  /** "chat" for a blank chat (no persona, instructions or memory) */
  kind?: string;
  /** a sub-agent made for one delegated task (lives in its parent's task panel, not in the list) */
  ephemeral?: boolean;
  parent?: string;
}

export interface SessionMeta {
  id: string;
  title?: string;
  kind: "agent" | "channel" | string;
  agents?: string[];
  folder?: string;
  /** channel's team ("" / unset = the default team) */
  team?: string;
  parent?: string;
  topic?: string;
  created: string;
}

export type TeamVisibility = "public" | "private";
export interface TeamView {
  id: string;
  name: string;
  color?: string;
  visibility: TeamVisibility;
  people: { id: string; role: "lead" | "member" | "viewer" }[];
  created: string;
  default: boolean;
  agents: number;
  /** blank chats (not counted in agents) */
  chats?: number;
  channels: number;
  defaultMode?: "" | "read-only" | "sandbox";
  tokens: number;
  /** for the person asking */
  myRole: "" | "lead" | "member" | "viewer";
  canAct: boolean;
  canManage: boolean;
}
export type TeamRole = "lead" | "member" | "viewer";

export const DEFAULT_TEAM = "default";
/** The team an agent or channel belongs to. */
export const teamOf = (x?: { team?: string }) => x?.team || DEFAULT_TEAM;

export interface LastLine {
  type: string;
  text: string;
  agent?: string;
  time: string;
}
export interface SessionSummary extends SessionMeta {
  updated: string;
  last?: LastLine;
  events: number;
  /** the last event the person has seen (any device) */
  read?: number;
  /** replies after it */
  unread?: number;
}

export interface ChannelView extends SessionMeta {
  owner: string;
  state: "active" | "paused" | "archived" | string;
  members: string[];
  settings: { handoverConfirm: boolean };
}

export interface SessionEvent {
  v: number;
  seq: number;
  time: string;
  session: string;
  type: string;
  agent?: string;
  data?: any;
}

export interface ToolCall {
  id: string;
  name: string;
  args: any;
}
export interface ToolResult {
  callId: string;
  name?: string;
  content: string;
  isError?: boolean;
  /** workspace images the tool showed the model (e.g. a window screenshot) */
  attachments?: Attachment[];
}
export interface Part {
  type: "text" | "thinking" | "tool_call" | "tool_result" | "citation" | "server_tool" | string;
  text?: string;
  meta?: Record<string, string>;
  toolCall?: ToolCall;
  toolResult?: ToolResult;
}
export interface Message {
  role: string;
  parts: Part[] | null;
}

export interface ApprovalRequest {
  callId: string;
  session: string;
  agent: string;
  tool: string;
  category: string;
  summary: string;
  reason: string;
  /** longer context shown on demand (e.g. what a workflow gate passes on) */
  preview?: string;
  asked: string;
  rememberable: boolean;
  confirmRemember: boolean;
}

export interface ModelInfo {
  id: string;
  provider: string;
  name?: string;
  contextWindow: number;
  capabilities?: string[];
  /** What the model produces: chat (default), image or video. */
  kind?: ModelKind | string;
  enabled: boolean;
  /** most tokens one reply may have, when known */
  maxOutput?: number;
  /** API list price (USD per million tokens; image: per image), from the model catalog */
  pricing?: ModelPricing;
  /** reasoning levels it takes; null/absent = not known (all offered), [] = none */
  efforts?: string[] | null;
}
export interface ModelPricing {
  input?: number;
  output?: number;
  cacheRead?: number;
  cacheWrite?: number;
  image?: number;
  source?: string;
}
export type ModelKind = "chat" | "image" | "video";
export interface Quota {
  known: boolean;
  remaining: number;
  resetsAt?: string;
}
export interface ProviderView {
  id: string;
  kind: "api" | "local" | string;
  models: ModelInfo[];
  quota?: Quota;
  error?: string;
}
export interface ModelSwitches {
  provider: string;
  /** every model of the provider; enabled = its switch */
  models: ModelInfo[];
}
export interface ModelState {
  model: string;
  until: string;
  reason: string;
}
export interface ModelsResponse {
  providers: ProviderView[];
  exhausted?: ModelState[] | null;
  switches?: number;
}

export interface StreamChunk {
  agent: string;
  session: string;
  chunk: { type: string; text?: string; toolCall?: ToolCall };
}

export interface ContextSegment {
  key: "system" | "memory" | "tools" | "skills" | "toolio" | "messages" | "free" | string;
  label: string;
  tokens: number;
}
export interface ContextBreakdown {
  agent: string;
  session: string;
  model: ModelRef;
  window: number;
  threshold: number;
  /** the provider's count when known (see counted), else estimated */
  used: number;
  /** sum of the estimated segments (what auto-compaction acts on) */
  estimated: number;
  free: number;
  percent: number;
  overThreshold: boolean;
  segments: ContextSegment[];
  compactions: number;
  /** used is the provider's count of the last request (plus what was added since) */
  counted?: boolean;
  /** segments and items come from the request the agent loop actually sent */
  recorded?: boolean;
  /** prompt cache of the last request, when the provider reported cache fields */
  cache?: ContextCache;
  /** requests left before auto-compaction at the average growth; 0 / absent = unknown */
  headroom?: number;
  /** blocks of the context map; per kind they add up to the segment */
  items?: ContextItem[];
}
export interface ContextCache {
  /** every input token of the request, cache reads and writes included */
  input: number;
  read: number;
  write: number;
  hitPercent: number;
}
export interface ContextItem {
  id: string;
  /** a segment key: system | memory | skills | tools | toolio | messages | other */
  kind: string;
  label: string;
  /** tools: MCP server / tool group; tool calls: an argument preview; messages: the role */
  group?: string;
  tokens: number;
  /** a roll-up ("N more") of count items */
  count?: number;
}
export interface ContextItemPart {
  title: "text" | "description" | "parameters" | "arguments" | "result" | "items" | string;
  format: "markdown" | "json" | "text" | string;
  text: string;
}
export interface ContextItemContent extends ContextItem {
  parts: ContextItemPart[];
  truncated?: boolean;
}

/** A page shown in the right panel's 预览 tab (features/preview Opened). */
export interface PreviewInfo {
  title: string;
  /** for kind "path" possibly stale (a stopped server): reopen the path */
  url: string;
  kind: "path" | "url";
  /** workspace-relative file or folder */
  path?: string;
  agent?: string;
  source: string;
  call?: string;
  time: string;
}

/** A visual reply in the conversation (show_page, features/preview/page.go). */
/** A message found by ⌘K search (features/console/search.go). */
export interface SearchHit {
  session: string;
  seq: number;
  agent?: string;
  from?: string;
  snippet: string;
  time: string;
}

/** A message replied to (回复): its seq, who wrote it, an excerpt. */
export interface Quote {
  seq: number;
  who: string;
  text: string;
}

export interface PageShown {
  title: string;
  path: string;
  url: string;
  agent?: string;
  call?: string;
  checked: boolean;
  notes?: string[];
  time: string;
}

export interface FileEntry {
  name: string;
  dir: boolean;
  size: number;
  modified: string;
  /** a folder with an index.html (it can be previewed) */
  index?: boolean;
  /** a folder with a package.json: an app run by its dev server */
  project?: boolean;
}
/** 文件 › 改动 (features/console/changes.go) */
export interface FileChange {
  path: string;
  from?: string;
  status: "added" | "modified" | "deleted" | "renamed";
  added: number;
  removed: number;
  binary?: boolean;
  large?: boolean;
  updated?: string;
}
export interface ChangesView {
  source: "git" | "tracked";
  branch?: string;
  files: FileChange[];
  added: number;
  removed: number;
  more?: boolean;
}
export interface DiffLine {
  t: " " | "-" | "+";
  o?: number;
  n?: number;
  s: string;
}
export interface Hunk {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: DiffLine[];
}
/** 这一轮改了哪些文件 (features/console/snapshots.go) */
export interface TurnSummary {
  turn: string;
  files: number;
  added: number;
  removed: number;
  undone?: boolean;
  /** of files: the images / videos it generated (shown in the conversation) */
  media?: number;
}
export interface TurnChanges {
  turn: string;
  at: string;
  files: FileChange[];
  added: number;
  removed: number;
  partial?: boolean;
  restorable: boolean;
  undone?: string;
  later?: string[];
}
export interface FileDiff extends FileChange {
  hunks: Hunk[];
}
export interface FileContent {
  path: string;
  size: number;
  content?: string;
  binary?: boolean;
  tooLarge?: boolean;
}

export interface RememberedChoice {
  agent: string;
  category: string;
  at: string;
  overridden?: boolean;
}
export interface ApprovalHistoryItem extends ApprovalRequest {
  decision?: { outcome: string; message?: string; remember?: boolean; by?: string };
  decided?: string;
}

export interface Settings {
  defaultMode: "read-only" | "sandbox" | string;
  defaultModels: string[];
  /** How the person is called in the UI (empty = "你"). */
  displayName?: string;
  /** Do-not-disturb until this RFC 3339 time; "" = off. */
  dndUntil?: string;
  /** Settings › 通知: only cells the person changed, plus quiet hours. */
  notify?: NotifyPrefs | null;
  /** Folders created from the list (kept while empty). */
  folders?: string[];
}

export interface QuietHours {
  on: boolean;
  start: string; // "HH:MM"
  end: string;
}

export interface NotifyPrefs {
  matrix?: Record<string, Record<string, boolean>>;
  quiet: QuietHours;
}

/** Interface slots (GET /api/ui). Declarative: rendered by us, as text. */
export interface UIField {
  label: string;
  value: string;
  href?: string;
  mono?: boolean;
}

export interface UIPanel {
  sections: { title?: string; text?: string; fields?: UIField[] }[];
  empty?: string;
}

export interface UIContribution {
  slot: "conversation.node" | "sidebar-right.tab" | "settings.card" | "palette.command" | "composer.action";
  key: string;
  title?: string;
  desc?: string;
  titleEn?: string;
  descEn?: string;
  icon?: string;
  order?: number;
  owner: string;
  card?: { title: string; tone?: "info" | "ok" | "warn" | "error"; icon?: string; fields?: UIField[] };
  confirm?: string;
}

/** 导入角色模板 (POST /api/agents/import[/preview]). Data is base64. */
export interface ImportSource {
  files?: { path: string; data: string }[];
  zip?: string;
  text?: string;
}

export interface RoleTemplate {
  format: "hermes" | "markdown" | "json" | "text";
  name: string;
  role?: string;
  instructions: string;
  skills: { name: string; description?: string; files: string[] }[];
  skipped: { path: string; reason: string }[];
}

export interface ImportOptions {
  name?: string;
  role?: string;
  folder?: string;
  mode?: string;
  color?: string;
  shape?: string;
  face?: string;
  modelRefs?: string[];
}

/** GET /api/notify: effective state for every kind × connected channel. */
export interface NotifyState {
  kinds: string[];
  channels: string[];
  matrix: Record<string, Record<string, boolean>>;
  quiet: QuietHours;
  quietNow: boolean;
  dndUntil?: string;
}

/** GET /api/system */
export interface SystemInfo {
  version?: string;
  startedAt?: string;
  hostname?: string;
  os?: string;
  arch?: string;
  dataDir?: string;
}

/** Injected by the Crewbase desktop shell (MyGo window). Absent in a browser. */
export interface CrewbaseDesktop {
  version?: string;
  platform?: "darwin" | "windows" | "linux";
  quit?: () => void;
  openExternal?: (url: string) => void;
  setBadge?: (n: number) => void;
  /** Native folder chooser; resolves to "" when cancelled. */
  pickFolder?: (start?: string) => Promise<string>;
  /** closing the window keeps the app running (Settings › 通用) */
  keepRunning?: () => Promise<boolean>;
  setKeepRunning?: (on: boolean) => Promise<boolean>;
  /** in-place updates: the state now, and every change of it (returns off) */
  updateState?: () => Promise<DesktopUpdateState>;
  onUpdate?: (fn: (st: DesktopUpdateState) => void) => () => void;
  /** checks now, in place (no window); a version found downloads when autoDownload is on */
  checkForUpdates?: () => void;
  /** downloads the version found, or retries what failed */
  downloadUpdate?: () => void;
  /** relaunches into the update once it is ready (agents stop first) */
  restartToUpdate?: () => Promise<void>;
  setAutoUpdate?: (on: boolean) => Promise<DesktopUpdateState>;
  setLanguage?: (l: "zh" | "en") => void;
  /** macOS permissions the app uses (Settings › 通用 › 系统权限) */
  permissions?: () => Promise<DesktopPermission[]>;
  requestPermission?: (id: string) => Promise<DesktopPermission[]>;
  openPermissionSettings?: (id: string) => void;
}

/** Computer use (features/computertools): GET /api/computer. */
export type ComputerPerm = "allow" | "ask" | "block";
export interface ComputerLive {
  agent: string;
  app: string;
  appName?: string;
  action: string;
  foreground?: boolean;
  paused?: boolean;
  since: string;
}
export interface ComputerState {
  live: ComputerLive[];
  stoppedAll: boolean;
  stopped: string[];
}
export interface ComputerApp {
  id: string;
  name: string;
  running: boolean;
  blocked?: boolean;
  browser?: boolean;
  /** agent → allow | block ("ask" is the default and not listed) */
  perms: Record<string, ComputerPerm>;
}
export interface ComputerBlocked {
  id: string;
  name: string;
  group: string;
  key: string;
  blocked: boolean;
}
export interface ComputerOverview {
  available: boolean;
  platform: string;
  enabled: boolean;
  permissions: { accessibility: boolean; screenRecording: boolean };
  apps: ComputerApp[];
  blocks: { default: ComputerBlocked[]; user: ComputerBlocked[]; unblocked: string[] };
  state: ComputerState;
  appsError?: string;
}

export interface DesktopPermission {
  id: "notifications" | "microphone" | "full-disk-access" | "accessibility" | "screen-recording";
  status: "granted" | "denied" | "not-determined" | "unknown";
  canRequest: boolean;
}

/** The desktop app's update, pushed by the shell (apps/desktop/updates.go). */
export interface DesktopUpdateState {
  state: "disabled" | "idle" | "checking" | "available" | "downloading" | "ready" | "error";
  /** the running version */
  current: string;
  /** the new version */
  version?: string;
  /** its release notes, Markdown */
  notes?: string;
  /** 0…1 while downloading; 1 while it is unpacked */
  progress: number;
  downloaded?: number;
  total?: number;
  error?: string;
  errorDuring?: "check" | "download";
  /** the person asked for the last check */
  user?: boolean;
  /** the last check found nothing newer */
  upToDate?: boolean;
  autoDownload: boolean;
  lastCheck?: string;
}

export interface UpdateInfo {
  current: string;
  latest?: string;
  available: boolean;
  url?: string;
  notes?: string;
  checked?: string;
  error?: string;
  enabled: boolean;
}

declare global {
  interface Window {
    crewbaseDesktop?: CrewbaseDesktop;
  }
}

export function desktop(): CrewbaseDesktop | undefined {
  return typeof window !== "undefined" ? window.crewbaseDesktop : undefined;
}

/** Open a URL outside the app: the desktop shell's browser, or a new tab. */
export function openExternal(url: string) {
  const d = desktop();
  if (d?.openExternal) d.openExternal(url);
  else window.open(url, "_blank", "noopener,noreferrer");
}
export interface TaskRoute {
  task: string;
  model: string;
}

export interface HostView {
  id: string;
  name: string;
  online: boolean;
  os: string;
  arch: string;
  local?: boolean;
}

export interface Routine {
  id: string;
  name: string;
  agent: string;
  prompt: string;
  schedule: string;
  mode: string;
  session: string;
  enabled: boolean;
  secret?: string;
  nextRun?: string;
  lastRun?: string;
  /** "agent": set up by the agent in a conversation (origin) */
  by?: string;
  origin?: string;
}
export interface RoutineRun {
  routine: string;
  key: string;
  input: string;
  status: "running" | "quiet" | "attention" | "failed" | string;
  summary?: string;
  started: string;
  ended?: string;
}
export interface Brief {
  time: string;
  needsYou: ApprovalRequest[] | null;
  attention: RoutineRun[] | null;
  failedAgents: { agent: string; name: string; detail: string }[] | null;
  folded: number;
}

/** A / pick sent with a message: a skill, a family of tools, or an MCP
 *  prompt template ("<server>/<prompt>", filled in with args). */
export interface SlashPick {
  kind: "skill" | "tools" | "prompt";
  name: string;
  args?: Record<string, string>;
}
export interface PromptInfo {
  server: string;
  name: string;
  title?: string;
  description?: string;
  args?: { name: string; description?: string; required?: boolean }[];
}

export interface WfStep {
  id: string;
  name?: string;
  kind: "agent" | "parallel" | "approval" | string;
  agent?: string;
  prompt?: string;
  mode?: string;
  parallel?: WfStep[];
  approval?: { summary?: string };
}
export interface Workflow {
  id: string;
  name: string;
  description?: string;
  trigger: string;
  steps: WfStep[];
  created?: string;
  updated?: string;
  hook?: string;
  secret?: string;
  nextRun?: string;
  lastRun?: string;
}
export interface WfStepView {
  id: string;
  group?: string;
  kind: string;
  agent?: string;
  status: string;
  output?: string;
  error?: string;
  reused?: boolean;
  started?: string;
  ended?: string;
}
export interface WfRun {
  id: string;
  workflow: string;
  name: string;
  status: string;
  trigger?: string;
  input?: string;
  rerunOf?: string;
  fromStep?: string;
  output?: string;
  error?: string;
  started: string;
  ended?: string;
  steps: WfStepView[];
}

export interface McpStatus {
  state: "connecting" | "ready" | "error" | "disabled" | string;
  tools: number;
  error?: string;
  pid?: number;
  enforcement?: string;
  since?: string;
  refreshError?: string;
  /** state "login": where the person signs in (OAuth servers) */
  loginUrl?: string;
  /** what the server says it is when it connects */
  version?: string;
}
export interface McpServer {
  name: string;
  transport: "stdio" | "http" | string;
  command?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
  scope: string;
  enabled: boolean;
  trust?: string;
  source?: string;
  oauth?: { clientId?: string; clientSecret?: string };
  /** which of the server's tools are registered (globs); unset = all */
  tools?: McpToolFilter | null;
  status: McpStatus;
}
export interface McpToolFilter {
  include?: string[];
  exclude?: string[];
}
export interface McpTool {
  name: string;
  original: string;
  description?: string;
  category: string;
  /** the server's tool filter lets it through */
  enabled?: boolean;
  schemaSize?: number;
  /** input schema over ~8k characters */
  large?: boolean;
}
/** A connector agents can be given (GET /api/connectors). */
export interface ConnectorInfo {
  name: string;
  label?: string;
  kind: "mcp" | "builtin" | string;
  scope: string;
  tools: number;
}

export interface Permissions {
  services?: string[];
  events?: string[];
  network?: string[];
  credentials?: string[];
  files?: string[];
  subprocess?: boolean;
  /** runs without the sandbox (the manifest opted out, or a person allowed it) */
  unsandboxed?: boolean;
}
/** A vault entry (Settings › 保管库): never its value. */
export interface VaultItem {
  id: string;
  ref: string;
  masked: string;
  kind: "provider" | "model" | "mcp" | "mcp-login" | "im" | "secret" | "app";
  target?: string;
  key?: string;
  /** an app's entry: app "agentdocs", role admin / session / agent:<name> */
  app?: string;
  role?: string;
  /** deleting it breaks something that runs now */
  inUse?: boolean;
  created: string;
  updated: string;
}
export interface PluginView {
  name: string;
  version: string;
  description?: string;
  author?: string;
  repo?: string;
  source: string;
  sourceKind: string;
  installedAt: string;
  dir: string;
  sandbox: boolean;
  requested: Permissions;
  grant?: { version: string; permissions: Permissions; trustFutureVersions: boolean; approvedAt: string };
  approved: boolean;
  needsApproval?: string[];
  enabled: boolean;
  state: string;
  error?: string;
  process?: { state: string; pid?: number; restarts: number; tools?: string[]; enforcement: string; limits?: string[] };
  integrity?: string;
  /** set only when a trusted publisher signature was verified */
  publisher?: { id: string; name: string };
}
export interface MarketEntry {
  name: string;
  description: string;
  version: string;
  source: string;
  permissions: Permissions;
  author?: string;
  repo?: string;
  installed?: string;
}

export interface BrowserState {
  agent: string;
  running: boolean;
  url?: string;
  title?: string;
  takenOverBy?: string;
  /** true when the takeover is held by this client */
  mine?: boolean;
  since?: string;
  headless: boolean;
  view?: "frames" | "window" | string;
  /** a takeover moved to a desktop window (back to headless on hand-back) */
  window?: boolean;
  /** the takeover can move to a desktop window */
  canWindow?: boolean;
  /** …and this client runs where that window would open */
  windowHere?: boolean;
}
export interface Screenshot {
  mime: string;
  data: string;
  width: number;
  height: number;
  url?: string;
  title?: string;
}
export type BrowserInput =
  | { type: "click"; x: number; y: number }
  | { type: "key"; text: string }
  | { type: "navigate"; url: string }
  | { type: "scroll"; dy: number };

export interface UsageStats {
  from: string;
  tokens: number;
  inputTokens: number;
  outputTokens: number;
  messages: number;
  peakDay?: string;
  peakTokens: number;
  days: Record<string, number>;
  byProvider: { provider: string; kind: string; tokens: number }[];
  byAgent: { agent: string; name: string; model: string; tokens: number }[];
  switches: number;
  interventions: number;
  /** estimated API spend (direct keys and gateways) */
  spentUsd: number;
  /** part of spentUsd that went through a gateway such as magpie */
  gatewayUsd?: number;
  reference: { input: number; output: number };
  kinds: Record<string, number>;
  /** images / videos generated per provider (billed by the provider per item) */
  generated?: { provider: string; images: number; videos: number }[];
}

// ---------------------------------------------------------------- token

const TOKEN_KEY = "crewbase.token";

export function initToken(): string | null {
  const url = new URL(window.location.href);
  const q = url.searchParams.get("token");
  if (q) {
    try {
      localStorage.setItem(TOKEN_KEY, q);
    } catch {
      /* storage unavailable: keep in memory */
    }
    memToken = q;
    url.searchParams.delete("token");
    window.history.replaceState(null, "", url.pathname + url.search + url.hash);
    return q;
  }
  return getToken();
}

let memToken: string | null = null;

export function getToken(): string | null {
  if (memToken) return memToken;
  try {
    memToken = localStorage.getItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
  return memToken;
}

export function setToken(t: string | null) {
  memToken = t;
  try {
    if (t) localStorage.setItem(TOKEN_KEY, t);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
}

// ---------------------------------------------------------------- fetch

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await apiFetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  let data: any = null;
  const text = await res.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const e = data?.error ?? {};
    if (res.status === 401) onUnauthorized();
    throw new ApiError(res.status, e.code ?? "http_" + res.status, e.message ?? res.statusText);
  }
  return data as T;
}

/** Uploads one file (raw body) into the agent workspace. */
export async function uploadFile(agent: string, file: Blob, name: string): Promise<Attachment> {
  const headers: Record<string, string> = { "Content-Type": file.type || "application/octet-stream" };
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  const res = await apiFetch(`/api/agents/${enc(agent)}/uploads?name=${encodeURIComponent(name)}`, { method: "POST", headers, body: file });
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    if (res.status === 401) onUnauthorized();
    const e = data?.error ?? {};
    throw new ApiError(res.status, e.code ?? "http_" + res.status, e.message ?? res.statusText);
  }
  return data as Attachment;
}

/** URL of a workspace file (images inline, other files download). Over a
 *  relay the file has to come through the tunnel: use useRawURL. */
export function rawURL(agent: string, path: string): string {
  return `/api/agents/${enc(agent)}/raw?path=${encodeURIComponent(path)}&token=${encodeURIComponent(getToken() ?? "")}`;
}

/** rawURL that also works over a relay (an object URL fetched through the tunnel). */
export function useRawURL(agent: string | undefined, path: string): string | undefined {
  const direct = agent ? rawURL(agent, path) : undefined;
  const [url, setUrl] = useState<string | undefined>(viaRelay() ? undefined : direct);
  useEffect(() => {
    if (!agent || !viaRelay()) {
      setUrl(direct);
      return;
    }
    let live = true;
    let made: string | undefined;
    tunnelBlobURL(`/api/agents/${enc(agent)}/raw?path=${encodeURIComponent(path)}`, getToken() ?? "").then(
      (u) => {
        made = u;
        if (live) setUrl(u);
        else URL.revokeObjectURL(u);
      },
      () => live && setUrl(undefined),
    );
    return () => {
      live = false;
      if (made) URL.revokeObjectURL(made);
    };
  }, [agent, path]);
  return url;
}

// ---------------------------------------------------------------- 文档库

export type LibFolder = "inbox" | "media" | "docs" | "pages";
export type LibSource = "upload" | "generated" | "agent" | "person";

/** A document of the team library (features/library). */
export interface LibItem {
  id: number;
  team: string;
  folder: LibFolder;
  name: string;
  title?: string;
  mime: string;
  size: number;
  sha256: string;
  source: LibSource;
  agent?: string;
  session?: string;
  tags?: string[];
  private?: boolean;
  note?: string;
  created: string;
  updated: string;
  text: boolean;
  versions?: number;
  /** search hit: the matching passage, the match in «» */
  snippet?: string;
  /** search hit: where in the document (section / page) */
  passage?: { start: number; heading?: string };
  /** search hit: only its summary or keywords matched */
  bySummary?: boolean;
  /** written by a model when summaries are on (item detail) */
  summary?: string;
  keywords?: string[];
}

/** A document's tie to the workspace file it was published from. */
export interface LibLink {
  item: number;
  agent: string;
  path: string;
  /** the conversation (and message) it was published from */
  session?: string;
  seq?: number;
  /** when the two last matched */
  at: string;
  /** same | file (the workspace is newer) | doc (the library is newer) | both | file-gone; "" another member's agent */
  state: "same" | "file" | "doc" | "both" | "file-gone" | "";
}

export interface LibFileLink extends LibLink {
  name: string;
  title?: string;
}

export interface LibStats {
  total: number;
  folders: Partial<Record<LibFolder, number>>;
  sources: Partial<Record<LibSource, number>>;
  tags: { tag: string; n: number }[];
}

export interface LibVersion {
  id: number;
  size: number;
  agent?: string;
  source: string;
  at: string;
}

export interface LibQuery {
  q?: string;
  folder?: string;
  source?: string;
  tag?: string;
  limit?: number;
  offset?: number;
}

function libQS(team: string, extra: Record<string, string | number | undefined> = {}): string {
  const p = new URLSearchParams({ team });
  for (const [k, v] of Object.entries(extra)) if (v !== undefined && v !== "") p.set(k, String(v));
  return p.toString();
}

/** URL of a library file (or an earlier version); see useLibRawURL for relays. */
export function libRawURL(team: string, id: number, version?: number): string {
  return `/api/library/${id}/raw?${libQS(team, { version, token: getToken() ?? "" })}`;
}

/** libRawURL that also works over a relay. */
export function useLibRawURL(team: string, id: number, version?: number): string | undefined {
  const direct = libRawURL(team, id, version);
  const [url, setUrl] = useState<string | undefined>(viaRelay() ? undefined : direct);
  useEffect(() => {
    if (!viaRelay()) {
      setUrl(direct);
      return;
    }
    let live = true;
    let made: string | undefined;
    tunnelBlobURL(`/api/library/${id}/raw?${libQS(team, { version })}`, getToken() ?? "").then(
      (u) => {
        made = u;
        if (live) setUrl(u);
        else URL.revokeObjectURL(u);
      },
      () => live && setUrl(undefined),
    );
    return () => {
      live = false;
      if (made) URL.revokeObjectURL(made);
    };
  }, [team, id, version]);
  return url;
}

/** The text of a library file as stored (Markdown, code, …). */
export async function libRawText(team: string, id: number, version?: number): Promise<string> {
  const headers: Record<string, string> = {};
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  const res = await apiFetch(`/api/library/${id}/raw?${libQS(team, { version })}`, { headers });
  if (!res.ok) throw new ApiError(res.status, "http_" + res.status, res.statusText);
  return res.text();
}

/** Adds a file to the library (the person's own). */
export async function uploadLibrary(team: string, file: Blob, name: string, folder?: string): Promise<LibItem> {
  const headers: Record<string, string> = { "Content-Type": file.type || "application/octet-stream" };
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  const res = await apiFetch(`/api/library?${libQS(team, { name, folder })}`, { method: "POST", headers, body: file });
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    if (res.status === 401) onUnauthorized();
    const e = data?.error ?? {};
    throw new ApiError(res.status, e.code ?? "http_" + res.status, e.message ?? res.statusText);
  }
  return data as LibItem;
}

/** The start of a docs page, for a card in a conversation. */
export interface DocPageCard {
  id: string;
  url: string;
  title: string;
  icon?: string;
  updated?: number;
  markdown: string;
  total: number;
  shown: number;
}

/** Open Agent Docs connection (features/agentdocs). */
export interface AgentDocsStatus {
  connected: boolean;
  settings: { url: string; workspace: string; workspaceName?: string; agents?: string[]; scope?: string; wake: boolean };
  identities: { agent: string; id: string; name: string; after?: number }[];
  error?: string;
  /** the admin token is gone from the vault: repairAgentDocs fixes it */
  needsAdmin?: boolean;
  woken: number;
  /** the server Crewbase runs (一键启动) */
  local: { supported: boolean; release: string; running: boolean; url?: string; email?: string; version?: string; step?: string; error?: string };
}

/** An available update of an add-on (features/addonupdates). */
export interface AddonUpdate {
  kind: "plugin" | "skill" | "app";
  id: string;
  agent?: string;
  name: string;
  desc?: string;
  /** plugins only: apps and skills show no version */
  current?: string;
  latest?: string;
  notes?: string;
  source?: string;
  /** the update asks for more; it waits for approval */
  newPermissions?: string[];
}

export interface AddonUpdates {
  items: AddonUpdate[];
  checked?: string;
  error?: string;
}

export interface DeviceView {
  id: string;
  name: string;
  tier: "mobile" | "desktop";
  platform?: string;
  created: string;
  lastSeen?: string;
  via?: "lan" | "relay";
  member?: string;
}

export interface MemberView {
  id: string;
  name: string;
  role: "admin" | "member" | "viewer";
  approve: string[];
  created: string;
  devices?: number;
}

export interface TaskView {
  id: string;
  title: string;
  detail?: string;
  owner?: string;
  status: "todo" | "doing" | "blocked" | "done";
  deps?: string[];
  scope?: string[];
  note?: string;
  by: string;
  created: string;
  updated: string;
  ready: boolean;
  waiting?: string[];
}

export interface BoardView {
  root: string;
  tasks: TaskView[];
}

export interface TaskChange {
  title?: string;
  detail?: string;
  owner?: string;
  status?: string;
  deps?: string[];
  scope?: string[];
  note?: string;
}

export interface PairingCode {
  code: string;
  tier: string;
  expires: string;
}

export interface RelayStatus {
  configured: boolean;
  hostId?: string;
  status?: { relay: string; hostId: string; online: boolean; devices: number; error?: string };
}

export interface ImPlatform {
  id: string;
  name: string;
  nameEn?: string;
  channelEn?: string;
  app: boolean;
  appId: boolean;
  channel: string;
  tokenDoc: string;
  domain?: string;
  /** 飞书 / Lark: one product, two open platforms (the first is the default). */
  regions?: ImRegion[];
}

export interface ImRegion {
  id: string;
  name: string;
  nameEn?: string;
  brand: string;
  brandEn?: string;
  domain: string;
  tokenDoc: string;
}

export interface ImGateway {
  id: string;
  platform: string;
  channel: string;
  allowUsers: string[];
  appId?: string;
  region?: string;
  relayReplies: boolean;
  approvals: boolean;
  enabled: boolean;
  hasToken: boolean;
  hasAppToken?: boolean;
  connected?: boolean;
  running: boolean;
  error?: string;
  listen?: { agent: string; keywords?: string[] } | null;
  team?: string;
}

export type Followup = { id: string; agent: string; session: string; note: string; due: string; set: string };
export type WorkTask = { name: string; title: string; updated: string };
export type WorkNote = { name: string; title: string; size: number; updated: string };
export type ToldEntry = { time: string; session: string; via: string; text: string };
export type Working = { tasks: WorkTask[]; notes: WorkNote[]; told: ToldEntry[] };

export const API = {
  steer: (agent: string, input: string) => api<AgentView>("POST", `/api/agents/${enc(agent)}/queue/${enc(input)}/steer`),
  addonUpdates: () => api<AddonUpdates>("GET", "/api/addons/updates"),
  checkAddonUpdates: () => api<AddonUpdates>("POST", "/api/addons/updates/check"),
  applyAddonUpdate: (b: { kind: string; id: string; agent?: string }) => api<{ result: any; state: AddonUpdates }>("POST", "/api/addons/update", b),
  agentDocs: () => api<AgentDocsStatus>("GET", "/api/agentdocs"),
  /** a new admin token: the docs password (a server Crewbase runs) or a pasted one */
  repairAgentDocs: (b: { password?: string; token?: string }) => api<AgentDocsStatus>("POST", "/api/agentdocs/repair", b),
  agentDocsPage: (url: string) => api<DocPageCard>("GET", `/api/agentdocs/page?url=${encodeURIComponent(url)}&blocks=12`),
  /** signs this browser in to the docs when Crewbase holds the session */
  embedAgentDocs: () => api<{ url: string; signedIn: boolean }>("POST", "/api/agentdocs/embed"),
  connectAgentDocs: (b: { url: string; token: string; workspace?: string; agents?: string[]; wake?: boolean }) => api<AgentDocsStatus>("POST", "/api/agentdocs", b),
  patchAgentDocs: (b: { agents?: string[]; wake?: boolean }) => api<AgentDocsStatus>("PATCH", "/api/agentdocs", b),
  syncAgentDocs: () => api<AgentDocsStatus>("POST", "/api/agentdocs/sync"),
  startAgentDocs: (b: { email: string; password: string }) => api<AgentDocsStatus>("POST", "/api/agentdocs/local", b),
  disconnectAgentDocs: () => api<AgentDocsStatus>("DELETE", "/api/agentdocs"),
  library: (team: string, q: LibQuery = {}) => api<{ items: LibItem[]; stats?: LibStats }>("GET", `/api/library?${libQS(team, q as Record<string, string | number | undefined>)}`),
  libraryItem: (team: string, id: number) => api<{ item: LibItem; versions: LibVersion[] }>("GET", `/api/library/${id}?${libQS(team)}`),
  librarySettings: () => api<{ summaries: boolean }>("GET", "/api/library/settings"),
  setLibrarySettings: (b: { summaries: boolean }) => api<{ summaries: boolean }>("PUT", "/api/library/settings", b),
  libraryText: (team: string, id: number) => api<{ text: string }>("GET", `/api/library/${id}/text?${libQS(team)}`),
  patchLibrary: (team: string, id: number, b: { title?: string; tags?: string[]; private?: boolean }) => api<LibItem>("PATCH", `/api/library/${id}?${libQS(team)}`, b),
  deleteLibrary: (team: string, id: number) => api("DELETE", `/api/library/${id}?${libQS(team)}`),
  restoreLibrary: (team: string, id: number, version: number) => api<LibItem>("POST", `/api/library/${id}/restore?${libQS(team)}`, { version }),
  publishToLibrary: (team: string, b: { agent: string; path: string; session?: string; seq?: number; title?: string }) =>
    api<{ link: LibLink; item: LibItem }>("POST", `/api/library/publish?${libQS(team)}`, b),
  libraryLinks: (team: string, agent: string) => api<{ links: LibFileLink[] }>("GET", `/api/library/links?${libQS(team, { agent })}`),
  libraryLink: (team: string, id: number) => api<{ link: LibLink | null }>("GET", `/api/library/${id}/link?${libQS(team)}`),
  syncLibrary: (team: string, id: number) => api<{ link: LibLink; item: LibItem }>("POST", `/api/library/${id}/sync?${libQS(team)}`),
  pullLibrary: (team: string, id: number) => api<{ link: LibLink }>("POST", `/api/library/${id}/pull?${libQS(team)}`),
  unlinkLibrary: (team: string, id: number) => api("DELETE", `/api/library/${id}/link?${libQS(team)}`),
  attachLibrary: (team: string, id: number, agent: string) => api<Attachment>("POST", `/api/library/${id}/attach?${libQS(team)}`, { agent }),
  unfurl: (url: string) => api<LinkCard>("GET", `/api/unfurl?url=${enc(url)}`),
  updates: () => api<UpdateInfo>("GET", "/api/updates"),
  checkUpdates: () => api<UpdateInfo>("POST", "/api/updates/check"),
  teams: () => api<{ teams: TeamView[] }>("GET", "/api/teams"),
  createTeam: (b: { name: string; visibility: TeamVisibility; color?: string }) => api<TeamView>("POST", "/api/teams", b),
  teamMemory: (id: string) => api<{ text: string }>("GET", `/api/teams/${enc(id)}/memory`),
  setTeamMemory: (id: string, text: string) => api<{ text: string }>("PUT", `/api/teams/${enc(id)}/memory`, { text }),
  patchTeam: (id: string, b: { name?: string; visibility?: TeamVisibility; color?: string; people?: { id: string; role: TeamRole }[]; defaultMode?: string }) =>
    api<TeamView>("PATCH", `/api/teams/${enc(id)}`, b),
  joinTeam: (id: string) => api<TeamView>("POST", `/api/teams/${enc(id)}/join`),
  deleteTeam: (id: string) => api("DELETE", `/api/teams/${enc(id)}`),
  moveToTeam: (team: string, agent: string) => api<{ id: string; team: string }>("POST", `/api/teams/${enc(team)}/agents`, { agent }),
  setProjectDir: (agent: string, dir: string) => api<AgentView>("PATCH", `/api/agents/${enc(agent)}`, { projectDir: dir }),
  followups: (agent: string) => api<{ followups: Followup[] }>("GET", `/api/agents/${enc(agent)}/followups`),
  cancelFollowup: (agent: string, id: string) => api("DELETE", `/api/agents/${enc(agent)}/followups/${enc(id)}`),
  browseDirs: (path?: string) => api<{ path: string; parent: string; home: string; dirs: { name: string; path: string }[] }>("GET", `/api/fs/dirs?path=${enc(path ?? "")}`),
  working: (agent: string) => api<Working>("GET", `/api/agents/${enc(agent)}/working`),
  taskProgress: (agent: string, name: string) => api<{ name: string; text: string }>("GET", `/api/agents/${enc(agent)}/tasks/${enc(name)}`),
  note: (agent: string, name: string) => api<{ name: string; text: string }>("GET", `/api/agents/${enc(agent)}/notes/${enc(name)}`),
  putNote: (agent: string, name: string, text: string) => api<{ name: string }>("PUT", `/api/agents/${enc(agent)}/notes/${enc(name)}`, { text }),
  deleteNote: (agent: string, name: string) => api("DELETE", `/api/agents/${enc(agent)}/notes/${enc(name)}`),
  imConfig: () => api<{ platforms: ImPlatform[]; gateways: ImGateway[] }>("GET", "/api/im/config"),
  saveIm: (id: string, b: Record<string, unknown>) => api<ImGateway>("PUT", `/api/im/config/${enc(id)}`, b),
  removeIm: (id: string) => api("DELETE", `/api/im/config/${enc(id)}`),
  testIm: (id: string) => api<{ ok: boolean; error?: string }>("POST", `/api/im/${enc(id)}/test`),
  verifyIm: (b: { platform?: string; region?: string; appId?: string; botToken?: string; appToken?: string; id?: string }) =>
    api<{ ok: boolean; bot?: string; error?: string }>("POST", "/api/im/config/verify", b),
  imChats: (b: { platform?: string; region?: string; appId?: string; botToken?: string; id?: string }) =>
    api<{ chats: { id: string; name: string; description?: string; external?: boolean }[]; hint?: "permission" | "empty" | "unsupported" }>("POST", "/api/im/config/chats", b),
  health: () => api<{ ok: boolean }>("GET", "/api/health"),
  pair: (b: { code: string; name: string; platform: string }) => api<{ token: string; device: DeviceView }>("POST", "/api/pair", b),
  whoami: () => api<{ tier: string; device?: DeviceView }>("GET", "/api/whoami"),
  me: () => api<{ id: string; name: string; role: "owner" | "admin" | "member" | "viewer"; approve: string[] }>("GET", "/api/me"),
  members: () => api<{ members: MemberView[]; ownerDevices: number; roles: string[]; approvable: string[] }>("GET", "/api/members"),
  addTeamMember: (b: { name: string; role: string }) => api<MemberView>("POST", "/api/members", b),
  updateMember: (id: string, b: { name?: string; role?: string; approve?: string[] }) => api<MemberView>("PATCH", `/api/members/${enc(id)}`, b),
  removeMember: (id: string) => api<{ devices: number }>("DELETE", `/api/members/${enc(id)}`),
  board: (session: string) => api<BoardView>("GET", `/api/board?session=${encodeURIComponent(session)}`),
  addTask: (session: string, b: TaskChange) => api<TaskView>("POST", `/api/board/tasks?session=${encodeURIComponent(session)}`, b),
  updateTask: (session: string, id: string, b: TaskChange) => api<TaskView>("PATCH", `/api/board/tasks/${enc(id)}?session=${encodeURIComponent(session)}`, b),
  removeTask: (session: string, id: string) => api("DELETE", `/api/board/tasks/${enc(id)}?session=${encodeURIComponent(session)}`),
  devices: () => api<{ devices: DeviceView[]; pending: PairingCode[] }>("GET", "/api/devices"),
  startPairing: (tier: string, member = "") => api<PairingCode>("POST", "/api/devices/pairing", { tier, member }),
  cancelPairing: (code: string) => api("DELETE", `/api/devices/pairing/${enc(code)}`),
  renameDevice: (id: string, name: string) => api<DeviceView>("PATCH", `/api/devices/${enc(id)}`, { name }),
  revokeDevice: (id: string) => api("DELETE", `/api/devices/${enc(id)}`),
  relay: () => api<RelayStatus>("GET", "/api/relay"),
  relayPairLink: (code: string) => api<{ url: string }>("POST", "/api/relay/pair-link", { code }),
  agents: () => api<{ agents: AgentView[] }>("GET", "/api/agents"),
  agent: (id: string) => api<AgentView>("GET", `/api/agents/${enc(id)}`),
  createAgent: (b: Partial<AgentView> & { modelRefs?: string[]; template?: string; greet?: boolean }) => api<AgentView>("POST", "/api/agents", b),
  ui: () => api<{ slots: string[]; contributions: UIContribution[] }>("GET", "/api/ui"),
  uiPanel: (slot: string, key: string, session = "") => api<UIPanel>("POST", "/api/ui/panel", { slot, key, session, lang: getLang() }),
  uiRun: (slot: string, key: string, session = "") => api<{ toast?: string; open?: string }>("POST", "/api/ui/run", { slot, key, session, lang: getLang() }),
  previewImport: (src: ImportSource) => api<RoleTemplate>("POST", "/api/agents/import/preview", src),
  importAgent: (src: ImportSource, options: ImportOptions) =>
    api<{ agent: string; session: string; name: string; skills: number }>("POST", "/api/agents/import", { ...src, options }),
  patchAgent: (id: string, b: Record<string, unknown>) => api<AgentView>("PATCH", `/api/agents/${enc(id)}`, b),
  deleteAgent: (id: string) => api("DELETE", `/api/agents/${enc(id)}`),
  send: (id: string, b: { text: string; model?: string; session?: string; front?: boolean; queue?: boolean; attachments?: { path: string; name: string }[]; reply?: Quote; use?: SlashPick[] }) =>
    api<Input>("POST", `/api/agents/${enc(id)}/messages`, b),
  /** Regenerate (no text: resend seq and the messages after it as they
   *  were) or edit and resend (text: only seq, edited). History from seq on
   *  no longer reaches the agent's model; the log keeps it. */
  resend: (id: string, b: { session: string; seq: number; text?: string; model?: string }) =>
    api<{ rewind: number; from: number; reason: string; inputs: Input[] }>("POST", `/api/agents/${enc(id)}/resend`, b),
  action: (id: string, a: "stop" | "pause" | "resume") => api<AgentView>("POST", `/api/agents/${enc(id)}/${a}`),
  cancelInput: (id: string, input: string) => api<AgentView>("DELETE", `/api/agents/${enc(id)}/queue/${enc(input)}`),
  sessions: () => api<{ sessions: SessionMeta[] }>("GET", "/api/sessions"),
  events: (id: string, since: number) =>
    api<{ meta: SessionMeta; events: SessionEvent[] }>("GET", `/api/sessions/${enc(id)}/events?since=${since}`),
  approvals: () => api<{ pending: ApprovalRequest[] }>("GET", "/api/approvals"),
  decide: (callId: string, b: { outcome: "allowed-once" | "rejected"; message?: string; remember?: boolean; confirmed?: boolean }) =>
    api("POST", `/api/approvals/${enc(callId)}`, b),
  models: () => api<ModelsResponse>("GET", "/api/models"),

  summaries: () => api<{ sessions: SessionSummary[] }>("GET", "/api/sessions/summary"),
  context: (id: string, session?: string) =>
    api<ContextBreakdown>("GET", `/api/agents/${enc(id)}/context${session ? "?session=" + enc(session) : ""}`),
  contextItem: (id: string, item: string, session?: string) =>
    api<ContextItemContent>("GET", `/api/agents/${enc(id)}/context/item?id=${enc(item)}${session ? "&session=" + enc(session) : ""}`),
  compact: (id: string, session?: string) => api("POST", `/api/agents/${enc(id)}/compact`, { session }),
  newConversation: (id: string) => api<SessionMeta>("POST", `/api/agents/${enc(id)}/sessions`, {}),
  files: (id: string, path: string) => api<{ entries: FileEntry[] }>("GET", `/api/agents/${enc(id)}/files?path=${enc(path)}`),
  file: (id: string, path: string) => api<FileContent>("GET", `/api/agents/${enc(id)}/file?path=${enc(path)}`),
  changes: (id: string) => api<ChangesView>("GET", `/api/agents/${enc(id)}/changes`),
  fileDiff: (id: string, path: string) => api<FileDiff>("GET", `/api/agents/${enc(id)}/changes/diff?path=${enc(path)}`),
  revertChange: (id: string, path: string) => api<ChangesView>("POST", `/api/agents/${enc(id)}/changes/revert`, { path }),
  turnSummaries: (id: string) => api<TurnSummary[]>("GET", `/api/agents/${enc(id)}/turns`),
  turnChanges: (id: string, turn: string) => api<TurnChanges>("GET", `/api/agents/${enc(id)}/turns/${enc(turn)}`),
  turnDiff: (id: string, turn: string, path: string) => api<FileDiff>("GET", `/api/agents/${enc(id)}/turns/${enc(turn)}/diff?path=${enc(path)}`),
  restoreTurn: (id: string, turn: string) => api<{ restored: number }>("POST", `/api/agents/${enc(id)}/turns/${enc(turn)}/restore`),
  acceptChanges: (id: string) => api<ChangesView>("POST", `/api/agents/${enc(id)}/changes/accept`),
  saveFile: (id: string, path: string, content: string) => api("PUT", `/api/agents/${enc(id)}/file`, { path, content }),
  // 预览 (features/preview)
  openPreview: (id: string, path: string, session?: string, title?: string) => api<PreviewInfo>("POST", `/api/agents/${enc(id)}/preview`, { path, session, title }),
  previewHTML: (id: string, html: string, session?: string, title?: string) => api<PreviewInfo>("POST", `/api/agents/${enc(id)}/preview/html`, { html, session, title }),
  previews: (session: string) => api<{ previews: PreviewInfo[] }>("GET", `/api/sessions/${enc(session)}/previews`),

  remembered: () => api<{ remembered: RememberedChoice[] }>("GET", "/api/approvals/remembered"),
  forget: (agent: string, category: string) => api("DELETE", `/api/approvals/remembered/${enc(agent)}/${enc(category)}`),
  approvalHistory: (limit = 100) => api<{ history: ApprovalHistoryItem[] }>("GET", `/api/approvals/history?limit=${limit}`),

  resetModel: (model: string) => api("POST", "/api/models/reset", { model }),
  routes: () => api<{ routes: TaskRoute[]; tasks: string[]; inUse?: Record<string, string> }>("GET", "/api/router/routes"),
  setRoutes: (routes: TaskRoute[]) => api<{ routes: TaskRoute[] }>("PUT", "/api/router/routes", { routes }),
  settings: () => api<Settings>("GET", "/api/settings"),
  patchSettings: (b: Partial<Settings>) => api<Settings>("PATCH", "/api/settings", b),
  notify: () => api<NotifyState>("GET", "/api/notify"),
  computer: () => api<ComputerOverview>("GET", "/api/computer"),
  setComputer: (enabled: boolean) => api<ComputerOverview>("PATCH", "/api/computer", { enabled }),
  setComputerPermission: (agent: string, app: string, permission: ComputerPerm) =>
    api<ComputerOverview>("PUT", "/api/computer/permissions", { agent, app, permission }),
  setComputerBlocked: (key: string, blocked: boolean) => api<ComputerOverview>("PUT", "/api/computer/blocked", { key, blocked }),
  stopComputer: (agent?: string) =>
    api<ComputerState>("POST", agent ? `/api/agents/${enc(agent)}/computer/stop` : "/api/computer/stop"),
  resumeComputer: (agent?: string) => api<ComputerState>("POST", "/api/computer/resume", agent ? { agent } : {}),
  testNotify: (channel: string) => api<{ ok: boolean }>("POST", "/api/notify/test", { channel }),
  hosts: () => api<{ hosts: HostView[] }>("GET", "/api/hosts"),

  channels: () => api<{ channels: ChannelView[] }>("GET", "/api/channels"),
  channel: (id: string) => api<ChannelView>("GET", `/api/channels/${enc(id)}`),
  createChannel: (b: { name: string; members: string[]; folder?: string; handoverConfirm?: boolean }) => api<ChannelView>("POST", "/api/channels", b),
  patchChannel: (id: string, b: { handoverConfirm: boolean }) => api<ChannelView>("PATCH", `/api/channels/${enc(id)}`, b),
  postChannel: (id: string, text: string, reply?: Quote, use?: SlashPick[]) =>
    api<{ agent: string; input: string; position: number }>("POST", `/api/channels/${enc(id)}/messages`, { text, reply, use }),
  /** pin a message to the top of its conversation (remove: unpin) */
  pin: (session: string, target: number, remove?: boolean) => api<null>("POST", `/api/sessions/${enc(session)}/pins`, { target, remove: remove || undefined }),
  /** messages containing q in every conversation the caller sees */
  search: (q: string) => api<{ hits: SearchHit[] }>("GET", `/api/search?q=${enc(q)}`),
  /** the person has seen session up to seq (0: all of it) */
  markRead: (session: string, seq: number) => api<{ read: number }>("POST", `/api/sessions/${enc(session)}/read`, { seq }),
  /** an emoji on a message (features/reactions); remove takes yours back */
  react: (session: string, target: number, emoji: string, remove?: boolean) =>
    api<null>("POST", `/api/sessions/${enc(session)}/reactions`, { target, emoji, remove: remove || undefined }),
  providers: () => api<{ providers: UserProvider[]; presets: ProviderPreset[] }>("GET", "/api/providers"),
  testProvider: (b: ProviderInput) => api<ProviderTestResult>("POST", "/api/providers/test", b),
  addProvider: (b: ProviderInput & { models: string[]; kinds?: Record<string, ModelKind> }) => api<UserProvider>("POST", "/api/providers", b),
  removeProvider: (id: string) => api("DELETE", `/api/providers/${enc(id)}`),
  updateProvider: (id: string, b: ProviderPatch) => api<UserProvider>("PATCH", `/api/providers/${enc(id)}`, b),
  modelSwitches: (provider: string) => api<ModelSwitches>("GET", `/api/models/${encodeURIComponent(provider)}/switches`),
  setModelSwitch: (provider: string, model: string, enabled: boolean) =>
    api<ModelSwitches>("PUT", `/api/models/${encodeURIComponent(provider)}/switches`, { model, enabled }),
  magpieModels: () => api<MagpieChoice>("GET", "/api/providers/magpie/models"),
  setMagpieModels: (enabled: string[] | null) => api<MagpieChoice>("PUT", "/api/providers/magpie/models", { enabled }),
  modelCatalog: () => api<CatalogStatus>("GET", "/api/model-catalog"),
  refreshModelCatalog: () => api<CatalogStatus>("POST", "/api/model-catalog/refresh"),
  setModelCatalog: (b: { sources: string[]; autoRefresh: boolean }) => api<CatalogStatus>("PUT", "/api/model-catalog/settings", b),
  updateChannel: (id: string, b: { title?: string; folder?: string; handoverConfirm?: boolean }) => api<ChannelView>("PATCH", `/api/channels/${enc(id)}`, b),
  deleteChannel: (id: string) => api("DELETE", `/api/channels/${enc(id)}`),
  createFolder: (name: string) => api<{ name: string }>("POST", "/api/folders", { name }),
  renameFolder: (name: string, to: string) => api<{ moved: number }>("PATCH", `/api/folders/${enc(name)}`, { name: to }),
  dissolveFolder: (name: string) => api<{ moved: number }>("DELETE", `/api/folders/${enc(name)}`),
  channelAction: (id: string, a: "stop" | "resume" | "archive") => api<ChannelView>("POST", `/api/channels/${enc(id)}/${a}`),
  reassign: (id: string, to: string, note?: string) => api<ChannelView>("POST", `/api/channels/${enc(id)}/reassign`, { to, note }),
  addMember: (id: string, agent: string) => api<ChannelView>("POST", `/api/channels/${enc(id)}/members`, { agent }),

  routines: () => api<{ routines: Routine[] }>("GET", "/api/routines"),
  createRoutine: (b: Partial<Routine>) => api<Routine>("POST", "/api/routines", b),
  patchRoutine: (id: string, b: Partial<Pick<Routine, "name" | "prompt" | "schedule" | "enabled" | "mode">>) =>
    api<Routine>("PATCH", `/api/routines/${enc(id)}`, b),
  deleteRoutine: (id: string) => api("DELETE", `/api/routines/${enc(id)}`),
  runRoutine: (id: string) => api("POST", `/api/routines/${enc(id)}/run`),
  routineRuns: (id: string) => api<{ runs: RoutineRun[] }>("GET", `/api/routines/${enc(id)}/runs`),
  brief: () => api<{ preview: Brief; history: Brief[] }>("GET", "/api/brief"),

  workflows: () => api<{ workflows: Workflow[] }>("GET", "/api/workflows"),
  createWorkflow: (b: Workflow) => api<Workflow>("POST", "/api/workflows", b),
  putWorkflow: (id: string, b: Workflow) => api<Workflow>("PUT", `/api/workflows/${enc(id)}`, b),
  deleteWorkflow: (id: string) => api("DELETE", `/api/workflows/${enc(id)}`),
  runWorkflow: (id: string, input: string) => api<WfRun>("POST", `/api/workflows/${enc(id)}/run`, { input }),
  workflowRuns: (id: string) => api<{ runs: WfRun[] | null }>("GET", `/api/workflows/${enc(id)}/runs`),
  workflowRun: (run: string) => api<WfRun>("GET", `/api/workflow-runs/${enc(run)}`),
  rerun: (run: string, from: string) => api<WfRun>("POST", `/api/workflow-runs/${enc(run)}/rerun`, { from }),
  cancelRun: (run: string) => api("POST", `/api/workflow-runs/${enc(run)}/cancel`),

  mcp: () => api<{ servers: McpServer[] | null }>("GET", "/api/mcp"),
  mcpSignOut: (name: string) => api("POST", `/api/mcp/${enc(name)}/signout`),
  addMcp: (b: Partial<McpServer>) => api<McpServer>("POST", "/api/mcp", b),
  patchMcp: (name: string, b: Partial<McpServer>) => api<McpServer>("PATCH", `/api/mcp/${enc(name)}`, b),
  deleteMcp: (name: string) => api("DELETE", `/api/mcp/${enc(name)}`),
  probeMcp: (name: string) => api<{ ok: boolean; error?: string; tools: McpTool[] }>("POST", `/api/mcp/${enc(name)}/probe`),
  mcpTools: (name: string) => api<{ tools: McpTool[] | null; status: McpStatus }>("GET", `/api/mcp/${enc(name)}/tools`),
  connectors: () => api<{ connectors: ConnectorInfo[] | null }>("GET", "/api/connectors"),

  plugins: () => api<{ plugins: PluginView[] | null }>("GET", "/api/plugins/installed"),
  market: () => api<{ plugins: MarketEntry[] }>("GET", "/api/market"),
  install: (b: { source?: string; market?: string }) => api<PluginView>("POST", "/api/plugins/install", b),
  approvePlugin: (name: string, permissions: Permissions, trustFutureVersions: boolean) =>
    api<PluginView>("POST", `/api/plugins/${enc(name)}/approve`, { permissions, trustFutureVersions }),
  pluginAction: (name: string, a: "enable" | "disable" | "reload") => api<PluginView>("POST", `/api/plugins/${enc(name)}/${a}`),
  deletePlugin: (name: string) => api("DELETE", `/api/plugins/${enc(name)}`),

  browser: (id: string) => api<BrowserState>("GET", `/api/agents/${enc(id)}/browser`),
  takeover: (id: string) => api<BrowserState>("POST", `/api/agents/${enc(id)}/browser/takeover`),
  handback: (id: string) => api<BrowserState>("POST", `/api/agents/${enc(id)}/browser/handback`),
  /** Move a takeover to a desktop window (open) or back to the in-app frame view. */
  browserWindow: (id: string, open: boolean) => api<BrowserState>("POST", `/api/agents/${enc(id)}/browser/window`, { open }),
  screenshot: (id: string) => api<Screenshot>("GET", `/api/agents/${enc(id)}/browser/screenshot`),
  /** One frame as a PNG (an object URL the caller revokes); falls back to JSON on older daemons. */
  screenshotFrame: async (id: string): Promise<{ src: string; width: number; height: number; url: string; revoke: boolean }> => {
    const headers: Record<string, string> = {};
    const tok = getToken();
    if (tok) headers.Authorization = `Bearer ${tok}`;
    const res = await apiFetch(`/api/agents/${enc(id)}/browser/screenshot?raw=1`, { headers });
    const ct = res.headers.get("Content-Type") ?? "";
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      if (res.status === 401) onUnauthorized();
      throw new ApiError(res.status, data?.error?.code ?? "http_" + res.status, data?.error?.message ?? res.statusText);
    }
    if (ct.startsWith("application/json")) {
      const s = (await res.json()) as Screenshot;
      return { src: `data:${s.mime};base64,${s.data}`, width: s.width ?? 0, height: s.height ?? 0, url: s.url ?? "", revoke: false };
    }
    const src = URL.createObjectURL(await res.blob());
    const url = decodeURIComponent(res.headers.get("X-Crewbase-Url") ?? "");
    return { src, width: Number(res.headers.get("X-Crewbase-Width")) || 0, height: Number(res.headers.get("X-Crewbase-Height")) || 0, url, revoke: true };
  },
  browserInput: (id: string, b: BrowserInput) => api("POST", `/api/agents/${enc(id)}/browser/input`, b),

  usage: () => api<UsageStats>("GET", "/api/usage"),
  workspaceDir: () => api<{ dir: string; isDefault: boolean; default: string }>("GET", "/api/settings/workspace"),
  setWorkspaceDir: (dir: string) => api<{ dir: string; isDefault: boolean; default: string }>("PUT", "/api/settings/workspace", { dir }),
  agentPrompts: (id: string) => api<{ prompts: PromptInfo[] }>("GET", `/api/agents/${enc(id)}/prompts`),
  agentSkills: (id: string) => api<{ skills: { name: string; description?: string; source?: string }[] }>("GET", `/api/agents/${enc(id)}/skills`),
  agentToolGroups: (id: string) =>
    api<{ groups: { name: string; count: number; summary: string; deferred: boolean; installed?: boolean }[] | null; deferredTokens: number }>("GET", `/api/agents/${enc(id)}/tools`),
  vault: () => api<{ items: VaultItem[] }>("GET", "/api/vault"),
  addSecret: (name: string, value: string) => api<VaultItem>("POST", "/api/vault", { name, value }),
  replaceSecret: (id: string, value: string) => api<VaultItem>("PUT", "/api/vault", { id, value }),
  deleteSecret: (id: string) => api("DELETE", `/api/vault?id=${encodeURIComponent(id)}`),
  system: () => api<SystemInfo>("GET", "/api/system"),
};

function enc(s: string) {
  return encodeURIComponent(s);
}

export function refString(r: ModelRef, withEffort = true): string {
  return `${r.provider}/${r.model}${withEffort && r.effort ? "@" + r.effort : ""}`;
}

export function parseRef(s: string): ModelRef | null {
  let effort: string | undefined;
  const at = s.lastIndexOf("@");
  if (at > 0) {
    effort = s.slice(at + 1);
    s = s.slice(0, at);
  }
  const i = s.indexOf("/");
  if (i <= 0 || i === s.length - 1) return null;
  return { provider: s.slice(0, i), model: s.slice(i + 1), effort };
}

/** A provider offered in the add dialog. */
export interface ProviderPreset {
  id: string;
  name: string;
  api: string;
  baseURL: string;
  keyURL?: string;
  icon?: string;
  /** "" = model vendor, "platform" = aggregator / inference platform */
  group?: string;
  modelHint?: string;
}

/** A provider added from the UI (GET /api/providers). */
export interface UserProvider {
  id: string;
  name?: string;
  api: string;
  preset?: string;
  baseURL?: string;
  keyRef?: string;
  models?: string[];
  kinds?: Record<string, ModelKind>;
  icon?: string;
  masked?: string;
  active: boolean;
  error?: string;
}

/** PATCH /api/providers/{id}: omitted fields stay; a blank apiKey keeps the stored key. */
export interface ProviderPatch {
  name?: string;
  icon?: string;
  baseURL?: string;
  apiKey?: string;
  models?: string[];
  kinds?: Record<string, ModelKind>;
}

/** GET /api/providers/magpie/models */
export interface MagpieChoice {
  running: boolean;
  models: ModelInfo[];
  /** null = every model magpie lists */
  enabled: string[] | null;
}

/** GET /api/model-catalog */
export interface CatalogStatus {
  settings: { sources: string[]; autoRefresh: boolean };
  sources: { url: string; format?: string; fetched?: string; models: number; origin: "remote" | "embedded" | ""; error?: string }[];
  models: number;
  updated: string;
  refreshing: boolean;
  lastAttempt?: string;
  embeddedAt?: string;
}

export interface ProviderInput {
  id: string;
  name?: string;
  preset?: string;
  api: string;
  baseURL: string;
  apiKey?: string;
  /** vendor icon key, "mono" for a letter tile */
  icon?: string;
}

export interface ProviderTestResult {
  ok: boolean;
  latencyMs: number;
  models: ModelInfo[];
  error?: string;
  /** the key was accepted but models cannot be listed: type ids */
  manualModels?: boolean;
}

/** GET /api/unfurl: what a link card shows (features/linkpreview). */
export interface LinkCard {
  url: string;
  kind: "page" | "video" | "image" | "audio";
  title?: string;
  description?: string;
  siteName?: string;
  image?: string;
  icon?: string;
  embed?: { provider: string; src: string; aspect: number };
  media?: string;
}
