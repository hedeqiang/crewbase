// REST helpers and API types (mirrors features/api, core/session, core/agent,
// capabilities/approval). The page is served by crewd on the same origin.

export type Mode = "read-only" | "sandbox" | "host";
export type Status = "idle" | "running" | "waiting" | "paused" | "failed";

export interface ModelRef {
  provider: string;
  model: string;
  effort?: string;
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
  workspace: string;
  effectiveMode: Mode;
  nativeTools?: string[] | null;
}

export interface SessionMeta {
  id: string;
  title?: string;
  kind: "agent" | "channel" | string;
  agents?: string[];
  folder?: string;
  parent?: string;
  topic?: string;
  created: string;
}

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
  since?: string;
  headless: boolean;
  view?: "frames" | "window" | string;
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
  const res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
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

export const API = {
  health: () => api<{ ok: boolean }>("GET", "/api/health"),
  agents: () => api<{ agents: AgentView[] }>("GET", "/api/agents"),
  agent: (id: string) => api<AgentView>("GET", `/api/agents/${enc(id)}`),
  createAgent: (b: Partial<AgentView> & { modelRefs?: string[] }) => api<AgentView>("POST", "/api/agents", b),
  patchAgent: (id: string, b: Record<string, unknown>) => api<AgentView>("PATCH", `/api/agents/${enc(id)}`, b),
  deleteAgent: (id: string) => api("DELETE", `/api/agents/${enc(id)}`),
  send: (id: string, b: { text: string; model?: string; session?: string; front?: boolean }) =>
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
  hosts: () => api<{ hosts: HostView[] }>("GET", "/api/hosts"),

  channels: () => api<{ channels: ChannelView[] }>("GET", "/api/channels"),
  channel: (id: string) => api<ChannelView>("GET", `/api/channels/${enc(id)}`),
  createChannel: (b: { name: string; members: string[]; folder?: string }) => api<ChannelView>("POST", "/api/channels", b),
  patchChannel: (id: string, b: { handoverConfirm: boolean }) => api<ChannelView>("PATCH", `/api/channels/${enc(id)}`, b),
  postChannel: (id: string, text: string) =>
    api<{ agent: string; input: string; position: number }>("POST", `/api/channels/${enc(id)}/messages`, { text }),
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
  screenshot: (id: string) => api<Screenshot>("GET", `/api/agents/${enc(id)}/browser/screenshot`),
  browserInput: (id: string, b: BrowserInput) => api("POST", `/api/agents/${enc(id)}/browser/input`, b),

  usage: () => api<UsageStats>("GET", "/api/usage"),
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
