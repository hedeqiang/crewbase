// Global client state: a tiny external store (useSyncExternalStore) holding
// server data (agents, sessions, approvals, models, session event logs,
// live stream buffers) and UI state. Server data is only ever refreshed from
// the REST API or applied from SSE pushes; the conversation is a projection
// of the session events.

import { useMemo, useSyncExternalStore } from "react";
import {
  API,
  type AgentView,
  type Attachment,
  type ApprovalRequest,
  type BrowserState,
  type ChannelView,
  type SessionSummary,
  type ModelsResponse,
  type SessionEvent,
  type SessionMeta,
  type StreamChunk,
  type SystemInfo,
  type UIContribution,
  getToken,
} from "./api";
import { getLang, setLang as setLangRaw, t, type Lang } from "./i18n";

export type View = "chat" | "newAgent" | "settings";
/** Built-in tabs, or `ui:<key>` for a plugin's sidebar-right.tab. */
export type PanelTab = "task" | "files" | "agent" | `ui:${string}`;
export type Theme = "system" | "light" | "dark";
export type SettingsSection =
  | "general"
  | "profile"
  | "notify"
  | "keys"
  | "models"
  | "market"
  | "computer"
  | "auto"
  | "approvals"
  | "hosts";

export interface StreamBuf {
  text: string;
  thinking: string;
}

/** A file picked in the composer, uploading or ready to send. */
export interface PendingFile {
  id: number;
  name: string;
  size: number;
  mime: string;
  state: "up" | "ok" | "err";
  att?: Attachment;
  error?: string;
  /** object URL for an image preview (revoked when removed) */
  preview?: string;
}

export interface Toast {
  id: number;
  text: string;
}

export interface Profile {
  displayName: string;
  /** default model order for new agents (GET /api/settings) */
  defaultModels?: string[];
  dndUntil: string;
  system: SystemInfo | null;
}

export interface State {
  authed: boolean | null; // null = checking
  /** Folders created from the list; kept even while empty (settings.json). */
  savedFolders: string[];
  /** Interface slot contributions from plugins (GET /api/ui). */
  ui: UIContribution[];
  connected: boolean;
  agents: Record<string, AgentView>;
  sessions: Record<string, SessionMeta>;
  approvals: ApprovalRequest[];
  models: ModelsResponse | null;
  summaries: Record<string, SessionSummary>;
  channels: Record<string, ChannelView>;
  browser: Record<string, BrowserState>; // agent id → browser state
  newChannel: boolean;
  modelsTab: "providers" | "routing";
  /** File to open in the right panel's 文件 tab. */
  fileOpen: string | null;
  /** Bumped on SSE "mcp" pushes so settings pages can refetch. */
  mcpTick: number;
  events: Record<string, SessionEvent[]>;
  loadErr: Record<string, string>;
  streams: Record<string, StreamBuf>; // key `${session}|${agent}`
  view: View;
  current: string | null; // session id
  /** Recently opened conversations (MRU, oldest first) for ⌘[ / ⌘]. */
  hist: string[];
  histPos: number;
  /** Profile bits from GET /api/settings and /api/system (may be absent on older daemons). */
  profile: Profile;
  settings: SettingsSection;
  listOpen: boolean;
  panelOpen: boolean;
  panelTab: PanelTab;
  theme: Theme;
  lang: Lang;
  palette: boolean;
  overrides: Record<string, string | null>; // agentId → model ref for next messages
  drafts: Record<string, string>; // session → unsent text
  attach: Record<string, PendingFile[]>; // session → files picked for the next message
  hostConfirm: string | null; // agent id awaiting host-mode confirm
  toasts: Toast[];
}

const LS = {
  get(k: string): string | null {
    try {
      return localStorage.getItem("crewbase." + k);
    } catch {
      return null;
    }
  },
  set(k: string, v: string | null) {
    try {
      if (v === null) localStorage.removeItem("crewbase." + k);
      else localStorage.setItem("crewbase." + k, v);
    } catch {
      /* ignore */
    }
  },
};

const narrow = () => window.matchMedia("(max-width: 820px)").matches;
const midWidth = () => window.matchMedia("(max-width: 1100px)").matches;

const HIST_MAX = 30;

let state: State = {
  authed: null,
  connected: false,
  agents: {},
  sessions: {},
  approvals: [],
  models: null,
  summaries: {},
  channels: {},
  browser: {},
  newChannel: false,
  modelsTab: "providers",
  fileOpen: null,
  mcpTick: 0,
  events: {},
  loadErr: {},
  streams: {},
  view: "chat",
  current: LS.get("current"),
  hist: LS.get("current") ? [LS.get("current")!] : [],
  histPos: LS.get("current") ? 0 : -1,
  profile: { displayName: "", dndUntil: "", system: null },
  savedFolders: [],
  ui: [],
  settings: "general",
  listOpen: !narrow() || !LS.get("current"),
  panelOpen: !midWidth() && LS.get("panel") !== "0",
  panelTab: "task",
  theme: ((): Theme => {
    const v = LS.get("theme");
    return v === "light" || v === "dark" ? v : "system";
  })(),
  lang: getLang(),
  palette: false,
  overrides: {},
  drafts: {},
  attach: {},
  hostConfirm: null,
  toasts: [],
};

const subs = new Set<() => void>();

export function getState(): State {
  return state;
}

export function setState(patch: Partial<State> | ((s: State) => Partial<State>)) {
  const p = typeof patch === "function" ? patch(state) : patch;
  state = { ...state, ...p };
  if ("current" in p) LS.set("current", state.current);
  if ("panelOpen" in p && !midWidth()) LS.set("panel", state.panelOpen ? "1" : "0");
  subs.forEach((f) => f());
}

export function useStore<T>(sel: (s: State) => T): T {
  return useSyncExternalStore(
    (f) => {
      subs.add(f);
      return () => subs.delete(f);
    },
    () => sel(state),
  );
}

// ---------------------------------------------------------------- UI actions

let toastId = 0;
export function toast(text: string) {
  const id = ++toastId;
  setState((s) => ({ toasts: [...s.toasts, { id, text }] }));
  setTimeout(() => setState((s) => ({ toasts: s.toasts.filter((x) => x.id !== id) })), 3200);
}

export function applyTheme(t: Theme) {
  if (t === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
}

export function setTheme(t: Theme) {
  LS.set("theme", t === "system" ? null : t);
  applyTheme(t);
  setState({ theme: t });
}

export function setLang(l: Lang) {
  setLangRaw(l);
  setState({ lang: l });
}

export function openSession(id: string) {
  setState((s) => {
    let hist = s.hist;
    let histPos = s.histPos;
    if (hist[histPos] !== id) {
      hist = [...hist.slice(0, histPos + 1).filter((x) => x !== id), id].slice(-HIST_MAX);
      histPos = hist.length - 1;
    }
    return { view: "chat", current: id, hist, histPos, listOpen: narrow() ? false : s.listOpen, palette: false };
  });
  void loadSession(id);
}

/** ⌘[ / ⌘]: step through recently opened conversations. */
export function goHistory(dir: -1 | 1) {
  const s = state;
  let i = s.histPos + dir;
  while (i >= 0 && i < s.hist.length && !s.sessions[s.hist[i]]) i += dir;
  if (i < 0 || i >= s.hist.length) return;
  setState({ view: "chat", current: s.hist[i], histPos: i, palette: false });
  void loadSession(s.hist[i]);
}

export function openAgent(agentId: string) {
  const a = state.agents[agentId];
  if (a?.session) openSession(a.session);
}

export function openSettings(sec: SettingsSection = "general") {
  setState({ view: "settings", settings: sec, palette: false, listOpen: narrow() ? false : state.listOpen });
}

/** Deep links the desktop shell (tray, notifications, ⌘,) sets as the
 *  location hash: #/session/<id>, #/settings, #/settings/<section>. */
export function applyHashRoute(hash = location.hash): boolean {
  const m = /^#\/(session|settings)(?:\/([^/?#]+))?/.exec(hash);
  if (!m) return false;
  if (m[1] === "session" && m[2]) openSession(decodeURIComponent(m[2]));
  else if (m[1] === "settings") openSettings(((m[2] && decodeURIComponent(m[2])) || "general") as SettingsSection);
  else return false;
  // consume it so the same link can be followed again
  history.replaceState(null, "", location.pathname + location.search);
  return true;
}

export function openNewAgent() {
  setState({ view: "newAgent", palette: false, listOpen: narrow() ? false : state.listOpen });
}

// ---------------------------------------------------------------- server sync

/** The agent a session talks to: the agent itself, or a channel's owner. */
export function sessionAgent(sessionId: string | null): AgentView | undefined {
  if (!sessionId) return undefined;
  const meta = state.sessions[sessionId];
  const ch = state.channels[sessionId];
  if (ch) return state.agents[ch.owner] ?? state.agents[ch.members?.[0] ?? ""];
  const id = meta?.agents?.[0];
  if (id && state.agents[id]) return state.agents[id];
  return Object.values(state.agents).find((a) => a.session === sessionId);
}

export async function refreshAgents() {
  const r = await API.agents();
  const agents: Record<string, AgentView> = {};
  for (const a of r.agents) agents[a.id] = a;
  setState({ agents });
}

export async function refreshSessions() {
  const r = await API.sessions();
  const sessions: Record<string, SessionMeta> = {};
  for (const m of r.sessions) sessions[m.id] = m;
  setState((s) => ({
    sessions,
    // forget history entries for sessions that no longer exist
    hist: s.hist.filter((t) => sessions[t]),
    histPos: Math.min(s.histPos, s.hist.filter((t) => sessions[t]).length - 1),
    current: s.current && sessions[s.current] ? s.current : null,
  }));
}

export async function refreshSummaries() {
  try {
    const r = await API.summaries();
    const summaries: Record<string, SessionSummary> = {};
    for (const m of r.sessions ?? []) summaries[m.id] = m;
    setState({ summaries });
  } catch {
    /* older daemon: fall back to local snippets */
  }
}

export async function refreshChannels() {
  try {
    const r = await API.channels();
    const channels: Record<string, ChannelView> = {};
    for (const c of r.channels ?? []) channels[c.id] = c;
    setState({ channels });
  } catch {
    /* ignore */
  }
}

export async function refreshChannel(id: string) {
  try {
    const c = await API.channel(id);
    setState((s) => ({ channels: { ...s.channels, [id]: c } }));
  } catch {
    /* ignore */
  }
}

export function openNewChannel() {
  setState({ newChannel: true, palette: false });
}

let sumTimer: number | undefined;
/** Debounced summary refresh (snippets, order, topics). */
export function scheduleSummaries(ms = 600) {
  window.clearTimeout(sumTimer);
  sumTimer = window.setTimeout(() => void refreshSummaries(), ms);
}

export async function refreshApprovals() {
  try {
    const r = await API.approvals();
    setState({ approvals: r.pending ?? [] });
  } catch {
    /* ignore */
  }
}

export async function refreshModels() {
  try {
    const r = await API.models();
    setState({ models: r });
  } catch {
    /* ignore */
  }
}

const inflight = new Map<string, Promise<void>>();

export function loadSession(id: string): Promise<void> {
  const p = inflight.get(id);
  if (p) return p.then(() => loadSessionNow(id));
  return loadSessionNow(id);
}

function loadSessionNow(id: string): Promise<void> {
  const run = (async () => {
    const have = state.events[id] ?? [];
    const since = have.length ? have[have.length - 1].seq : 0;
    try {
      const r = await API.events(id, since);
      setState((s) => {
        const cur = s.events[id] ?? [];
        const last = cur.length ? cur[cur.length - 1].seq : 0;
        const add = r.events.filter((e) => e.seq > last);
        const { [id]: _, ...loadErr } = s.loadErr;
        return {
          events: add.length || !s.events[id] ? { ...s.events, [id]: [...cur, ...add] } : s.events,
          sessions: { ...s.sessions, [id]: r.meta },
          loadErr,
        };
      });
    } catch (e: any) {
      setState((s) => ({ loadErr: { ...s.loadErr, [id]: e?.message ?? String(e) } }));
    }
  })();
  inflight.set(id, run);
  return run.finally(() => {
    if (inflight.get(id) === run) inflight.delete(id);
  });
}

export async function refreshProfile() {
  const [st, sys] = await Promise.allSettled([API.settings(), API.system()]);
  setState((s) => ({
    profile: {
      displayName: st.status === "fulfilled" ? st.value.displayName ?? "" : s.profile.displayName,
      dndUntil: st.status === "fulfilled" ? st.value.dndUntil ?? "" : s.profile.dndUntil,
      defaultModels: st.status === "fulfilled" ? (st.value as { defaultModels?: string[] }).defaultModels : s.profile.defaultModels,
      system: sys.status === "fulfilled" ? sys.value : s.profile.system,
    },
    savedFolders: st.status === "fulfilled" ? st.value.folders ?? [] : s.savedFolders,
  }));
}

/** Every folder name: saved ones (possibly empty) and those in use. */
export function useFolders(): string[] {
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const saved = useStore((s) => s.savedFolders);
  return useMemo(
    () =>
      [...new Set([...saved, ...Object.values(agents).map((a) => a.folder), ...Object.values(sessions).map((m) => (m.kind === "agent" ? "" : m.folder))].filter(Boolean) as string[])].sort(
        (a, b) => a.localeCompare(b),
      ),
    [agents, sessions, saved],
  );
}

export async function patchProfile(b: { displayName?: string; dndUntil?: string }) {
  const prev = state.profile;
  setState((s) => ({ profile: { ...s.profile, ...b } }));
  try {
    const r = await API.patchSettings(b);
    setState((s) => ({ profile: { ...s.profile, displayName: r.displayName ?? s.profile.displayName, dndUntil: r.dndUntil ?? s.profile.dndUntil } }));
  } catch (e: any) {
    setState({ profile: prev });
    toast(e?.message ?? String(e));
  }
}

/** The name shown for the person ("你" when unset). */
export function myName(p: Profile): string {
  return p.displayName.trim() || t("common.you");
}

/** Whether do-not-disturb is on right now. */
export function dndActive(p: Profile, now = Date.now()): boolean {
  return !!p.dndUntil && new Date(p.dndUntil).getTime() > now;
}

export async function refreshUI() {
  try {
    const r = await API.ui();
    setState((s) => {
      const ui = r.contributions ?? [];
      // a plugin tab that went away falls back to the task tab
      const gone = s.panelTab.startsWith("ui:") && !ui.some((c) => c.slot === "sidebar-right.tab" && "ui:" + c.key === s.panelTab);
      return gone ? { ui, panelTab: "task" } : { ui };
    });
  } catch {
    /* older daemon without the ui service */
  }
}

export async function resync() {
  await Promise.allSettled([
    refreshAgents(),
    refreshSessions(),
    refreshApprovals(),
    refreshModels(),
    refreshSummaries(),
    refreshChannels(),
    refreshProfile(),
    refreshUI(),
  ]);
  for (const id of Object.keys(state.events)) void loadSession(id);
  if (state.current && !state.events[state.current]) void loadSession(state.current);
}

function streamKey(session: string, agent: string) {
  return `${session}|${agent}`;
}

function clearStream(session: string, agent?: string) {
  setState((s) => {
    const streams = { ...s.streams };
    let changed = false;
    for (const k of Object.keys(streams)) {
      if (k === streamKey(session, agent ?? "") || (!agent && k.startsWith(session + "|"))) {
        delete streams[k];
        changed = true;
      }
    }
    return changed ? { streams } : {};
  });
}

const SUMMARY_TYPES = new Set(["message/user", "message/assistant", "approval/asked", "session/meta"]);

function applySessionEvent(ev: SessionEvent) {
  if (!state.sessions[ev.session]) void refreshSessions();
  if (SUMMARY_TYPES.has(ev.type)) scheduleSummaries();
  if (ev.type.startsWith("channel/")) void refreshChannel(ev.session);
  if (ev.type === "session/meta") void refreshSessions();
  if (ev.type === "message/assistant" || ev.type === "turn/end") clearStream(ev.session, ev.agent);
  if (ev.type === "tool/call" && ev.agent) clearStream(ev.session, ev.agent);
  const cur = state.events[ev.session];
  if (!cur) return; // not loaded; will be fetched when opened
  const last = cur.length ? cur[cur.length - 1].seq : 0;
  if (ev.seq === last + 1) setState((s) => ({ events: { ...s.events, [ev.session]: [...(s.events[ev.session] ?? []), ev] } }));
  else if (ev.seq > last + 1) void loadSession(ev.session); // gap: refetch
}

function applyStream(e: StreamChunk) {
  const c = e.chunk;
  if (c.type !== "text" && c.type !== "thinking") return;
  const k = streamKey(e.session, e.agent);
  setState((s) => {
    const prev = s.streams[k] ?? { text: "", thinking: "" };
    const next = c.type === "text" ? { ...prev, text: prev.text + (c.text ?? "") } : { ...prev, thinking: prev.thinking + (c.text ?? "") };
    return { streams: { ...s.streams, [k]: next } };
  });
}

// ---------------------------------------------------------------- SSE

let es: EventSource | null = null;
let retry = 0;
let retryTimer: number | undefined;

export function connectEvents() {
  if (es) es.close();
  window.clearTimeout(retryTimer);
  const tok = getToken();
  if (!tok) return;
  const src = new EventSource(`/api/events?token=${encodeURIComponent(tok)}`);
  es = src;
  const on = (type: string, fn: (d: any) => void) =>
    src.addEventListener(type, (m) => {
      try {
        fn(JSON.parse((m as MessageEvent).data));
      } catch {
        /* ignore malformed */
      }
    });
  on("hello", () => {
    retry = 0;
    setState({ connected: true });
    void resync();
  });
  on("session.event", (d: SessionEvent) => applySessionEvent(d));
  on("agent.status", (d: { agent: string; status: AgentView["status"]; detail?: string }) => {
    setState((s) => {
      const a = s.agents[d.agent];
      if (!a) return {};
      return { agents: { ...s.agents, [d.agent]: { ...a, status: d.status, detail: d.detail } } };
    });
  });
  on("agent.updated", (d: AgentView) => {
    const isNew = !state.agents[d.id];
    setState((s) => ({ agents: { ...s.agents, [d.id]: d } }));
    if (isNew || !state.sessions[d.session]) void refreshSessions();
  });
  on("agent.deleted", (d: { id: string }) => {
    setState((s) => {
      const { [d.id]: gone, ...agents } = s.agents;
      const hist = gone ? s.hist.filter((t) => t !== gone.session) : s.hist;
      const current = gone && s.current === gone.session ? hist[hist.length - 1] ?? null : s.current;
      return { agents, hist, histPos: Math.min(s.histPos, hist.length - 1), current };
    });
  });
  on("agent.stream", (d: StreamChunk) => applyStream(d));
  on("approvals.changed", () => void refreshApprovals());
  on("channel.owner", (d: { channel: string }) => void refreshChannel(d.channel));
  on("browser.state", (d: BrowserState) => setState((s) => ({ browser: { ...s.browser, [d.agent]: d } })));
  on("mcp", () => setState((s) => ({ mcpTick: s.mcpTick + 1 })));
  on("models.changed", () => void refreshModels());
  on("ui.changed", () => void refreshUI());
  src.onerror = () => {
    setState({ connected: false });
    if (src.readyState === EventSource.CLOSED) {
      // Browser gave up (e.g. daemon restarted or token rejected): retry with backoff.
      retry = Math.min(retry + 1, 6);
      retryTimer = window.setTimeout(async () => {
        try {
          await API.health();
        } catch {
          /* still down or unauthorized: health() triggers the 401 handler */
        }
        if (getToken()) connectEvents();
      }, 500 * 2 ** retry);
    }
  };
}

export function disconnectEvents() {
  es?.close();
  es = null;
  window.clearTimeout(retryTimer);
}
