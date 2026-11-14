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
}

export interface SessionMeta {
  id: string;
  title?: string;
  kind: "agent" | "channel" | string;
  agents?: string[];
  folder?: string;
  parent?: string;
  created: string;
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
  type: "text" | "thinking" | "tool_call" | "tool_result" | string;
  text?: string;
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
