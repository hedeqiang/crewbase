// The API's shared shapes: agents, sessions and events, approvals, models
// and quota, context, files and changes, settings, the desktop bridge,
// routines and workflows, MCP, plugins and the vault, the browser, usage.

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
  /** 排队: waits for the running turn to end (else it joins it at the next step) */
  queue?: boolean;
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
  /** avatar style: unset = the crew character (color/shape/face), "vendor" = a model vendor's mark */
  avatar?: string;
  /** with avatar "vendor": unset = follow the model in use, else an icon key (ProviderMark) */
  vendor?: string;
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
  /** an MCP server asking the person mid-call (MCP elicitation): a form or a
   *  link to open; parent is the tool call it belongs to */
  elicit?: Elicitation;
  parent?: string;
}

/** What an MCP server asks for (features/mcp/elicit.go). */
export interface Elicitation {
  server: string;
  mode: "form" | "url";
  message: string;
  /** form: a flat JSON Schema (strings, numbers, booleans, enums) */
  schema?: ElicitSchema;
  url?: string;
}
export interface ElicitSchema {
  type?: "object";
  properties?: Record<string, ElicitField>;
  required?: string[];
}
export interface ElicitField {
  type?: "string" | "number" | "integer" | "boolean" | "array";
  title?: string;
  description?: string;
  format?: "email" | "uri" | "date" | "date-time" | string;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  enum?: string[];
  oneOf?: { const: string; title?: string }[];
  minItems?: number;
  maxItems?: number;
  items?: { type?: string; enum?: string[]; anyOf?: { const: string; title?: string }[] };
  default?: unknown;
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
  /** the allowance remaining is of, e.g. "codex · 7 days" */
  label?: string;
  windows?: { label: string; remaining: number; resetsAt?: string }[];
  /** the accounts behind a gateway, by vendor, with every allowance */
  accounts?: QuotaAccount[];
}
export interface QuotaAccount {
  vendor: string;
  vendorName?: string;
  account?: string;
  plan?: string;
  /** a prepaid account's balance as the vendor shows it */
  balance?: string;
  windows?: QuotaAccountWindow[];
}
export interface QuotaAccountWindow {
  name: string;
  /** share used, 0..1 */
  used: number;
  spent?: number;
  limit?: number;
  unit?: string;
  /** the vendor's own text for an allowance without a share */
  display?: string;
  text?: boolean;
  resetsAt?: string;
}
export interface ProviderView {
  id: string;
  kind: "api" | "local" | string;
  models: ModelInfo[];
  quota?: Quota;
  /** each model's own allowance (a gateway's models draw on different
   *  accounts); present = the provider reports per model, absent key = none */
  modelQuota?: Record<string, Quota>;
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
/** Settings › Open Artifact (features/openartifact). */
export interface OpenArtifactStatus {
  enabled: boolean;
  installed: string[];
  version?: string;
  latest?: string;
  node: { path?: string; version?: string; ok: boolean; problem?: string };
  running: boolean;
  daemon?: string;
  error?: string;
  home: string;
  homeUrl?: string;
  tools?: string[];
  sessions: number;
  busy?: string;
}

/** An artifact a turn made (open-artifact/artifact). */
export interface ArtifactCardInfo {
  url: string;
  title?: string;
  type?: string;
  root?: string;
  viewUrl?: string;
  action?: string;
  agent?: string;
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
/** 做成技能: a SKILL.md drafted from a stretch of conversation (POST …/skill-draft). */
export interface SkillDraft {
  name: string;
  description: string;
  /** the whole SKILL.md: front matter and body */
  content: string;
  model?: string;
  from: number;
  upto: number;
}

/** 从对话生成例行流程: a draft routine / workflow (POST …/automation-draft). */
export interface AutoDraft {
  kind: "routine" | "workflow";
  both?: boolean;
  routine: Routine;
  workflow: Workflow;
  task: string;
  quiet: string;
  tools?: string[];
  scheduleGuessed?: boolean;
  scheduleWhy?: string;
  model?: string;
  from: number;
  upto: number;
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
  /** skills the server publishes (MCP skills extension) */
  skills?: number;
  /** the server runs long calls as tasks (MCP tasks extension) */
  tasks?: boolean;
}
/** A skill an MCP server publishes (names and descriptions only). */
export interface McpSkill {
  server: string;
  uri: string;
  name: string;
  description?: string;
  files: number;
  size: number;
  dynamic?: boolean;
  invalid?: string;
  collides?: boolean;
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

/** One model's use (features/usage ModelShare). */
export interface ModelUse {
  model: string;
  provider: string;
  kind: string;
  tokens: number;
  input: number;
  output: number;
  messages: number;
  spentUsd: number;
  savedUsd: number;
  /** some tokens had no known price */
  unpriced?: boolean;
}

export interface UsageStats {
  from: string;
  tokens: number;
  inputTokens: number;
  outputTokens: number;
  messages: number;
  peakDay?: string;
  peakTokens: number;
  days: Record<string, number>;
  /** each day's spend */
  daysUsd?: Record<string, number>;
  byProvider: { provider: string; kind: string; tokens: number }[];
  byAgent: { agent: string; name: string; model: string; tokens: number; messages: number; spentUsd: number; models: ModelUse[]; deleted?: boolean }[];
  /** every model's use, most used first */
  byModel: ModelUse[];
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
