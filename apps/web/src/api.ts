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
  /** "chat" for a blank chat (no persona, instructions or memory) */
  kind?: string;
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
  enabled: boolean;
}
export interface Quota {
  known: boolean;
  remaining: number;
  resetsAt?: string;
}
export interface ProviderView {
  id: string;
  kind: "subscription" | "api" | "local" | string;
  models: ModelInfo[];
  quota?: Quota;
  error?: string;
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
  used: number;
  free: number;
  percent: number;
  overThreshold: boolean;
  segments: ContextSegment[];
  compactions: number;
}

export interface FileEntry {
  name: string;
  dir: boolean;
  size: number;
  modified: string;
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
  updates?: () => Promise<DesktopUpdates>;
  checkForUpdates?: () => void;
  setUpdatePrefs?: (checks: boolean, downloads: boolean) => Promise<DesktopUpdates>;
  setLanguage?: (l: "zh" | "en") => void;
}

export interface DesktopUpdates {
  version: string;
  enabled: boolean;
  automaticChecks: boolean;
  automaticDownloads: boolean;
  lastCheck?: string;
  available?: string;
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
  status: McpStatus;
}
export interface McpTool {
  name: string;
  original: string;
  description?: string;
  category: string;
}

export interface Permissions {
  services?: string[];
  events?: string[];
  network?: string[];
  credentials?: string[];
  files?: string[];
  subprocess?: boolean;
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
  savedUsd: number;
  spentUsd: number;
  reference: { input: number; output: number };
  kinds: Record<string, number>;
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
}

export interface ImGateway {
  id: string;
  platform: string;
  channel: string;
  allowUsers: string[];
  appId?: string;
  relayReplies: boolean;
  approvals: boolean;
  enabled: boolean;
  hasToken: boolean;
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
  working: (agent: string) => api<Working>("GET", `/api/agents/${enc(agent)}/working`),
  taskProgress: (agent: string, name: string) => api<{ name: string; text: string }>("GET", `/api/agents/${enc(agent)}/tasks/${enc(name)}`),
  note: (agent: string, name: string) => api<{ name: string; text: string }>("GET", `/api/agents/${enc(agent)}/notes/${enc(name)}`),
  putNote: (agent: string, name: string, text: string) => api<{ name: string }>("PUT", `/api/agents/${enc(agent)}/notes/${enc(name)}`, { text }),
  deleteNote: (agent: string, name: string) => api("DELETE", `/api/agents/${enc(agent)}/notes/${enc(name)}`),
  imConfig: () => api<{ platforms: ImPlatform[]; gateways: ImGateway[] }>("GET", "/api/im/config"),
  saveIm: (id: string, b: Record<string, unknown>) => api<ImGateway>("PUT", `/api/im/config/${enc(id)}`, b),
  removeIm: (id: string) => api("DELETE", `/api/im/config/${enc(id)}`),
  testIm: (id: string) => api<{ ok: boolean; error?: string }>("POST", `/api/im/${enc(id)}/test`),
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
  createAgent: (b: Partial<AgentView> & { modelRefs?: string[] }) => api<AgentView>("POST", "/api/agents", b),
  ui: () => api<{ slots: string[]; contributions: UIContribution[] }>("GET", "/api/ui"),
  uiPanel: (slot: string, key: string, session = "") => api<UIPanel>("POST", "/api/ui/panel", { slot, key, session, lang: getLang() }),
  uiRun: (slot: string, key: string, session = "") => api<{ toast?: string; open?: string }>("POST", "/api/ui/run", { slot, key, session, lang: getLang() }),
  previewImport: (src: ImportSource) => api<RoleTemplate>("POST", "/api/agents/import/preview", src),
  importAgent: (src: ImportSource, options: ImportOptions) =>
    api<{ agent: string; session: string; name: string; skills: number }>("POST", "/api/agents/import", { ...src, options }),
  patchAgent: (id: string, b: Record<string, unknown>) => api<AgentView>("PATCH", `/api/agents/${enc(id)}`, b),
  deleteAgent: (id: string) => api("DELETE", `/api/agents/${enc(id)}`),
  send: (id: string, b: { text: string; model?: string; session?: string; front?: boolean; attachments?: { path: string; name: string }[] }) =>
    api<Input>("POST", `/api/agents/${enc(id)}/messages`, b),
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
  compact: (id: string, session?: string) => api("POST", `/api/agents/${enc(id)}/compact`, { session }),
  newConversation: (id: string) => api<SessionMeta>("POST", `/api/agents/${enc(id)}/sessions`, {}),
  files: (id: string, path: string) => api<{ entries: FileEntry[] }>("GET", `/api/agents/${enc(id)}/files?path=${enc(path)}`),
  file: (id: string, path: string) => api<FileContent>("GET", `/api/agents/${enc(id)}/file?path=${enc(path)}`),
  saveFile: (id: string, path: string, content: string) => api("PUT", `/api/agents/${enc(id)}/file`, { path, content }),

  remembered: () => api<{ remembered: RememberedChoice[] }>("GET", "/api/approvals/remembered"),
  forget: (agent: string, category: string) => api("DELETE", `/api/approvals/remembered/${enc(agent)}/${enc(category)}`),
  approvalHistory: (limit = 100) => api<{ history: ApprovalHistoryItem[] }>("GET", `/api/approvals/history?limit=${limit}`),

  resetModel: (model: string) => api("POST", "/api/models/reset", { model }),
  routes: () => api<{ routes: TaskRoute[]; tasks: string[] }>("GET", "/api/router/routes"),
  setRoutes: (routes: TaskRoute[]) => api<{ routes: TaskRoute[] }>("PUT", "/api/router/routes", { routes }),
  settings: () => api<Settings>("GET", "/api/settings"),
  patchSettings: (b: Partial<Settings>) => api<Settings>("PATCH", "/api/settings", b),
  notify: () => api<NotifyState>("GET", "/api/notify"),
  testNotify: (channel: string) => api<{ ok: boolean }>("POST", "/api/notify/test", { channel }),
  hosts: () => api<{ hosts: HostView[] }>("GET", "/api/hosts"),

  channels: () => api<{ channels: ChannelView[] }>("GET", "/api/channels"),
  channel: (id: string) => api<ChannelView>("GET", `/api/channels/${enc(id)}`),
  createChannel: (b: { name: string; members: string[]; folder?: string }) => api<ChannelView>("POST", "/api/channels", b),
  patchChannel: (id: string, b: { handoverConfirm: boolean }) => api<ChannelView>("PATCH", `/api/channels/${enc(id)}`, b),
  postChannel: (id: string, text: string) =>
    api<{ agent: string; input: string; position: number }>("POST", `/api/channels/${enc(id)}/messages`, { text }),
  providers: () => api<{ providers: UserProvider[]; presets: ProviderPreset[] }>("GET", "/api/providers"),
  testProvider: (b: ProviderInput) => api<ProviderTestResult>("POST", "/api/providers/test", b),
  addProvider: (b: ProviderInput & { models: string[] }) => api<UserProvider>("POST", "/api/providers", b),
  removeProvider: (id: string) => api("DELETE", `/api/providers/${enc(id)}`),
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
  icon?: string;
  masked?: string;
  active: boolean;
  error?: string;
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
