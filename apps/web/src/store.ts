// Global client state: a tiny external store (useSyncExternalStore) holding
// server data (agents, sessions, approvals, models, session event logs,
// live stream buffers) and UI state. Server data is only ever refreshed from
// the REST API or applied from SSE pushes; the conversation is a projection
// of the session events.

import { useMemo, useSyncExternalStore } from "react";
import {
  API,
  DEFAULT_TEAM,
  teamOf,
  type TeamView,
  type AgentView,
  type Attachment,
  type ApprovalRequest,
  type BrowserState,
  type ComputerState,
  type ChannelView,
  type SessionSummary,
  type ModelsResponse,
  type SessionEvent,
  type SessionMeta,
  type StreamChunk,
  type SystemInfo,
  type UIContribution,
  type PreviewInfo,
  getToken,
  type Quote,
} from "./api";
import { tunnelEvents, viaRelay } from "./tunnel";
import { notifyApprovals } from "./companion";
import { getLang, loadLang, setLang as setLangRaw, t, type Lang } from "./i18n";
import { modelKind, setUserProviders } from "./format";

export type View = "chat" | "newAgent" | "settings";
/** Built-in tabs, or `ui:<key>` for a plugin's sidebar-right.tab. */
export type PanelTab = "task" | "schedule" | "files" | "preview" | "library" | "docs" | "agent" | `ui:${string}`;
export type Theme = "system" | "light" | "dark";
export type SettingsSection =
  | "general"
  | "profile"
  | "vault"
  | "notify"
  | "keys"
  | "models"
  | "market"
  | "library"
  | "docs"
  | "computer"
  | "auto"
  | "approvals"
  | "hosts"
  | "teams"
  | "im"
  | "team";

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

export interface Me {
  id: string;
  name: string;
  role: "owner" | "admin" | "member" | "viewer";
  approve: string[];
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
  /** What this client may do: "desktop" (full control) or "mobile"
   *  (view, approve, send) — paired phones (GET /api/whoami). */
  tier: "desktop" | "mobile" | "viewer";
  /** Who is using this client (GET /api/me): the owner or a team member. */
  me: Me | null;
  /** Bumped per board root on SSE "board.changed". */
  boardTick: Record<string, number>;
  /** Member to pair a device for when 主机与设备 opens (from 成员与角色). */
  pairFor: string | null;
  /** Sub-tab a deep link asks a settings page to open (e.g. market/mcp). */
  settingsTab: string | null;
  /** Folders created from the list; kept even while empty (settings.json). */
  savedFolders: string[];
  /** teams on this host, and the one the sidebar shows */
  teams: TeamView[];
  team: string;
  /** Interface slot contributions from plugins (GET /api/ui). */
  ui: UIContribution[];
  connected: boolean;
  agents: Record<string, AgentView>;
  /** the agent list has arrived once (until then nothing is "empty") */
  agentsLoaded: boolean;
  /** the conversation list has arrived once */
  sessionsLoaded: boolean;
  /** the address has been followed (router.ts startRouter) */
  routed: boolean;
  sessions: Record<string, SessionMeta>;
  approvals: ApprovalRequest[];
  models: ModelsResponse | null;
  /** the 连接模型 dialog is open (from the welcome page, composer or an error) */
  connecting: boolean;
  summaries: Record<string, SessionSummary>;
  channels: Record<string, ChannelView>;
  browser: Record<string, BrowserState>; // agent id → browser state
  /** who operates which app now (features/computertools); null until known */
  computer: ComputerState | null;
  newChannel: boolean;
  /** folder new things are created in (the folder's + / menu), once */
  createIn: string;
  modelsTab: "providers" | "routing" | "catalog";
  /** File to open in the right panel's 文件 tab. */
  fileOpen: string | null;
  /** the 文档库 document shown on the library page */
  libOpen: number | null;
  /** A page to show in the right panel's 预览 tab (n bumps per request). */
  previewOpen: { session: string; item: PreviewInfo; n: number } | null;
  /** Bumped on SSE "mcp" pushes so settings pages can refetch. */
  mcpTick: number;
  /** add-on updates waiting (插件与技能 badges) */
  addonUpdates: number;
  /** the connected Open Agent Docs server (null: none) */
  agentDocs: { url: string; workspace?: string; local: boolean } | null;
  /** the docs page shown in the right panel's 文档 tab (null: the docs home) */
  docsOpen: string | null;
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
  /** a sub-agent run (its session) shown in the right panel */
  subView: string | null;
  theme: Theme;
  /** your bubbles: neutral grey, or the agent's (channel's) colour, light or solid */
  bubbles: Bubbles;
  /** cards under links in messages (设置 › 通用), per browser */
  linkCards: boolean;
  lang: Lang;
  palette: boolean;
  overrides: Record<string, string | null>; // agentId → model ref for next messages
  drafts: Record<string, string>; // session → unsent text
  attach: Record<string, PendingFile[]>; // session → files picked for the next message
  replyTo: Record<string, Quote | undefined>; // session → the message the next one answers (回复)
  /** a message to scroll to and flash once its conversation shows it (search) */
  jump: { session: string; seq: number } | null;
  hostConfirm: string | null; // agent id awaiting host-mode confirm
  toasts: Toast[];
}

// The model chosen in a chat's composer survives a reload (per browser).
function loadOverrides(): Record<string, string | null> {
  try {
    const v = JSON.parse(localStorage.getItem("crewbase.modelOverrides") || "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}

/** Remembers (or clears) the composer's model choice for an agent. */
export function setModelOverride(agentId: string, ref: string | null) {
  setState((s) => {
    const overrides = { ...s.overrides, [agentId]: ref };
    if (ref === null) delete overrides[agentId];
    try {
      localStorage.setItem("crewbase.modelOverrides", JSON.stringify(overrides));
    } catch {
      /* private window: kept for this tab */
    }
    return { overrides };
  });
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

const MODELS_CACHE = "crewbase.models";

function cachedModels(): ModelsResponse | null {
  try {
    const v = JSON.parse(localStorage.getItem(MODELS_CACHE) || "null");
    return v && Array.isArray(v.providers) ? v : null;
  } catch {
    return null;
  }
}

// Unsent text stays when the app reloads (kept a moment after typing).
const DRAFTS_KEY = "crewbase.drafts";
function loadDrafts(): Record<string, string> {
  try {
    const v = JSON.parse(localStorage.getItem(DRAFTS_KEY) ?? "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}
let draftTimer: ReturnType<typeof setTimeout> | undefined;
function saveDrafts() {
  clearTimeout(draftTimer);
  draftTimer = setTimeout(() => {
    try {
      const keep = Object.fromEntries(Object.entries(state.drafts).filter(([, v]) => v && v.trim()).slice(-50));
      localStorage.setItem(DRAFTS_KEY, JSON.stringify(keep));
    } catch {
      /* private mode */
    }
  }, 400);
}

let state: State = {
  authed: null,
  tier: "desktop",
  me: null,
  boardTick: {},
  pairFor: null,
  settingsTab: null,
  connected: false,
  agents: {},
  agentsLoaded: false,
  sessionsLoaded: false,
  routed: false,
  sessions: {},
  approvals: [],
  // the last list this browser saw: the welcome page and pickers show it at
  // once instead of "looking for models", and refreshModels replaces it
  models: cachedModels(),
  connecting: false,
  summaries: {},
  channels: {},
  browser: {},
  computer: null,
  newChannel: false,
  createIn: "",
  modelsTab: "providers",
  fileOpen: null,
  libOpen: null,
  previewOpen: null,
  mcpTick: 0,
  addonUpdates: 0,
  agentDocs: null,
  docsOpen: null,
  events: {},
  loadErr: {},
  streams: {},
  // the address says where a reload lands: settings shows its own page
  // (and loading) at once, not a conversation's
  view: typeof location !== "undefined" && location.hash.startsWith("#/settings") ? "settings" : typeof location !== "undefined" && location.hash.startsWith("#/new-agent") ? "newAgent" : "chat",
  current: LS.get("current"),
  hist: LS.get("current") ? [LS.get("current")!] : [],
  histPos: LS.get("current") ? 0 : -1,
  profile: { displayName: "", dndUntil: "", system: null },
  savedFolders: [],
  teams: [],
  team: loadTeam(),
  ui: [],
  settings: ((typeof location !== "undefined" && /^#\/settings\/([\w-]+)/.exec(location.hash)?.[1]) || "general") as SettingsSection,
  listOpen: !narrow() || !LS.get("current"),
  panelOpen: !midWidth() && LS.get("panel") !== "0",
  panelTab: "task",
  subView: null,
  theme: ((): Theme => {
    const v = LS.get("theme");
    return v === "light" || v === "dark" ? v : "system";
  })(),
  bubbles: ((): Bubbles => {
    const v = LS.get("bubbles");
    return v === "tint" || v === "solid" ? v : "neutral";
  })(),
  linkCards: LS.get("linkCards") !== "off",
  lang: getLang(),
  palette: false,
  overrides: loadOverrides(),
  drafts: loadDrafts(),
  attach: {},
  replyTo: {},
  jump: null,
  hostConfirm: null,
  toasts: [],
};

const subs = new Set<() => void>();

export function getState(): State {
  return state;
}

/** Calls f after every change (the router follows the store with it). */
export function subscribe(f: () => void): () => void {
  subs.add(f);
  return () => subs.delete(f);
}

export function setState(patch: Partial<State> | ((s: State) => Partial<State>)) {
  const p = typeof patch === "function" ? patch(state) : patch;
  state = { ...state, ...p };
  if ("current" in p) LS.set("current", state.current);
  if ("drafts" in p) saveDrafts();
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
/** 回复: the next message in session answers q (null: not any more). */
export function setReply(session: string, q: Quote | null) {
  setState((s) => ({ replyTo: { ...s.replyTo, [session]: q ?? undefined } }));
}

export function toast(text: string) {
  const id = ++toastId;
  setState((s) => ({ toasts: [...s.toasts, { id, text }] }));
  setTimeout(() => setState((s) => ({ toasts: s.toasts.filter((x) => x.id !== id) })), 3200);
}

export function applyTheme(t: Theme) {
  if (t === "system") delete document.documentElement.dataset.theme;
  else document.documentElement.dataset.theme = t;
}

export type Bubbles = "neutral" | "tint" | "solid";

export function setBubbles(b: Bubbles) {
  LS.set("bubbles", b === "neutral" ? null : b);
  setState({ bubbles: b });
}

export function setLinkCards(on: boolean) {
  LS.set("linkCards", on ? null : "off");
  setState({ linkCards: on });
}

export function setTheme(t: Theme) {
  LS.set("theme", t === "system" ? null : t);
  applyTheme(t);
  setState({ theme: t });
}

export async function setLang(l: Lang) {
  await loadLang(l);
  setLangRaw(l);
  setState({ lang: l });
  // plugin entries and team names have per-language wording
  void refreshUI();
  void refreshTeams();
}

export function openSession(id: string) {
  setState((s) => {
    let hist = s.hist;
    let histPos = s.histPos;
    if (hist[histPos] !== id) {
      hist = [...hist.slice(0, histPos + 1).filter((x) => x !== id), id].slice(-HIST_MAX);
      histPos = hist.length - 1;
    }
    return { view: "chat", current: id, hist, histPos, listOpen: narrow() ? false : s.listOpen, palette: false, subView: id === s.current ? s.subView : null, fileOpen: id === s.current ? s.fileOpen : null };
  });
  void loadSession(id);
}

/** ⌘[ / ⌘]: step through recently opened conversations. */
/** ⌘[ / ⌘]: back and forward through the places shown (router.ts). */
export function goHistory(dir: -1 | 1) {
  history.go(dir);
}

export function openAgent(agentId: string) {
  const a = state.agents[agentId];
  if (a?.session) openSession(a.session);
}

export function openSettings(sec: SettingsSection = "general") {
  setState({ view: "settings", settings: sec, palette: false, listOpen: narrow() ? false : state.listOpen });
}

export const isChat = (a?: AgentView) => a?.kind === "chat";


/** The name the daemon gives a chat before anything else names it. */
const CHAT_DEFAULT_NAME = "新对话";

/** What a chat is called: its name (the daemon renames a chat after its
 *  generated topic, and a name the person gave it stays), else the topic
 *  while it still has the default name. */
export const chatTitle = (a: AgentView, topic?: string) => (a.name && a.name !== CHAT_DEFAULT_NAME ? a.name : topic || a.name);

// a function declaration (hoisted): the initial state reads it at load
function teamKey() {
  return "crewbase.team";
}

function loadTeam(): string {
  try {
    return localStorage.getItem(teamKey()) || DEFAULT_TEAM;
  } catch {
    return DEFAULT_TEAM;
  }
}

/** Shows another team in the sidebar (new agents, chats and channels go there). */
export function switchTeam(id: string) {
  try {
    localStorage.setItem(teamKey(), id);
  } catch {
    /* private mode */
  }
  setState((s) => {
    // a conversation of another team closes
    const meta = s.current ? s.sessions[s.current] : undefined;
    const owner = meta?.kind === "channel" ? meta : Object.values(s.agents).find((a) => a.session === s.current);
    const keep = !!owner && teamOf(owner) === id;
    return { team: id, current: keep ? s.current : null, view: s.view === "settings" ? s.view : "chat" };
  });
}

/** A new agent's run mode in the team on screen: read-only when the team
 *  starts its agents read-only, else m. */
export function teamMode<M extends string>(m: M): M | "read-only" {
  const tm = state.teams.find((x) => x.id === state.team);
  return tm?.defaultMode === "read-only" ? "read-only" : m;
}

/** The team new things are created in ("" = default, as the API stores it). */
export function currentTeam(): string | undefined {
  return state.team === DEFAULT_TEAM ? undefined : state.team;
}

export async function refreshAgentDocs() {
  try {
    const d = await API.agentDocs();
    setState({ agentDocs: d.connected ? { url: d.settings.url, workspace: d.settings.workspaceName, local: !!d.local?.running } : null });
  } catch {
    /* an older server */
  }
}

/** Whether href is a page of the connected docs server. */
export function isDocsLink(href: string): boolean {
  const d = state.agentDocs;
  if (!d) return false;
  try {
    return new URL(href).origin === new URL(d.url).origin;
  } catch {
    return false;
  }
}

/** The docs can be shown inside Crewbase: same host as this page, so the
 *  docs' sign-in cookie is a same-site one. */
export function docsEmbeddable(): boolean {
  const d = state.agentDocs;
  if (!d || typeof location === "undefined") return false;
  try {
    return new URL(d.url).hostname === location.hostname;
  } catch {
    return false;
  }
}

/** Opens a docs page (or the docs home) in the right panel. */
export function openDocs(href?: string) {
  setState({ panelOpen: true, panelTab: "docs", docsOpen: href ?? null });
}

export async function refreshAddonUpdates() {
  if (state.tier !== "desktop") return; // managing add-ons is the desktop's
  try {
    const u = await API.addonUpdates();
    setState({ addonUpdates: u.items.length });
  } catch {
    /* an older server: no reminders */
  }
}

export async function refreshTeams() {
  try {
    const r = await API.teams();
    // the default team's original name follows the interface language
    const teams = r.teams.map((x) => (x.default && x.name === "我的团队" ? { ...x, name: t("teams.defaultName") } : x));
    setState((s) => ({ teams, team: teams.some((x) => x.id === s.team) ? s.team : DEFAULT_TEAM }));
  } catch {
    /* an older daemon: no teams */
  }
}

/** 新对话: a blank chat on the default model, opened right away. */
/** Shows a page in the right panel's 预览 tab (the panel opens too). */
export function openPreview(session: string, item: PreviewInfo) {
  setState((s) => ({ panelOpen: true, panelTab: "preview", previewOpen: { session, item, n: (s.previewOpen?.n ?? 0) + 1 } }));
}

/** Opens a sub-agent's run in the right panel (the panel opens too). */
export function openSubRun(session: string) {
  setState({ subView: session, panelOpen: true, panelTab: "task" });
  void loadSession(session);
}

// the new chat being created, so a second 新对话 / ⌘N in the meantime opens
// it instead of creating another
let creatingChat: Promise<void> | null = null;

export async function newChat(folder?: string) {
  if (creatingChat) return creatingChat;
  creatingChat = newChatOnce(folder).finally(() => {
    creatingChat = null;
  });
  return creatingChat;
}

async function newChatOnce(folder?: string) {
  // an untouched new chat in the same place is reused: ⌘N twice does not
  // leave a row of empty "新对话"s behind
  const blank = Object.values(state.agents).find(
    (a) =>
      a.kind === "chat" &&
      teamOf(a) === currentTeam() &&
      (a.folder ?? "") === (folder ?? "") &&
      a.status !== "running" &&
      a.queue.length === 0 &&
      // no summary yet means nothing was said in it
      !state.summaries[a.session]?.last,
  );
  if (blank) {
    setState({ palette: false });
    openSession(blank.session);
    return;
  }
  try {
    const a = await API.createAgent({ kind: "chat", team: currentTeam(), folder: folder || undefined });
    setState((s) => ({ agents: { ...s.agents, [a.id]: a }, palette: false }));
    await refreshSessions();
    openSession(a.session);
  } catch (e: any) {
    toast(t("chat.newFailed", { error: e?.message ?? String(e) }));
  }
}

/** The team's 文档库 page. */
export function openLibrary(item?: number) {
  openSettings("library");
  setState({ libOpen: item ?? null });
}

export function openNewAgent(folder = "") {
  setState({ view: "newAgent", palette: false, createIn: folder, listOpen: narrow() ? false : state.listOpen });
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
  setState({ agents, agentsLoaded: true });
}

export async function refreshSessions() {
  const r = await API.sessions();
  const sessions: Record<string, SessionMeta> = {};
  for (const m of r.sessions) sessions[m.id] = m;
  setState((s) => ({
    sessions,
    sessionsLoaded: true,
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

export function openNewChannel(folder = "") {
  setState({ newChannel: true, palette: false, createIn: folder });
}

let readTimer: number | undefined;
/** The person has seen session up to seq: the list's badge goes at once,
 *  the daemon hears it a moment later (any device then shows it read). */
export function markRead(session: string, seq: number) {
  const sm = state.summaries[session];
  if (sm && (sm.unread || (sm.read ?? 0) < seq)) setState((s) => ({ summaries: { ...s.summaries, [session]: { ...sm, unread: 0, read: Math.max(sm.read ?? 0, seq) } } }));
  window.clearTimeout(readTimer);
  readTimer = window.setTimeout(() => void API.markRead(session, seq).catch(() => {}), 500);
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
    notifyApprovals(r.pending ?? [], (id) => state.agents[id]?.name ?? id);
  } catch {
    /* ignore */
  }
}

/** Whether any model can answer: null while the list is still loading. */
export function hasModels(s: State = state): boolean | null {
  if (!s.models) return null;
  return (s.models.providers ?? []).some((p) => (p.models ?? []).some((m) => m.enabled !== false && modelKind(m) === "chat"));
}

export const openConnect = () => setState({ connecting: true });

export async function refreshModels() {
  try {
    // names and icons of providers added from the UI (older daemons lack
    // the route), fetched alongside the models rather than before them
    const [up, r] = await Promise.all([API.providers().catch(() => null), API.models()]);
    if (up) setUserProviders(up.providers ?? []);
    setState({ models: r });
    try {
      localStorage.setItem(MODELS_CACHE, JSON.stringify(r));
    } catch {
      /* private window or full: not kept */
    }
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
  // a team member's device shows that member, not the host owner's profile
  const me = state.me;
  if (me && me.role !== "owner" && me.name) return me.name;
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
      // plugins may word their entries in English too
      const en = getLang() === "en";
      const ui = (r.contributions ?? []).map((c) => (en ? { ...c, title: c.titleEn || c.title, desc: c.descEn || c.desc } : c));
      // a plugin tab that went away falls back to the task tab
      const gone = s.panelTab.startsWith("ui:") && !ui.some((c) => c.slot === "sidebar-right.tab" && "ui:" + c.key === s.panelTab);
      return gone ? { ui, panelTab: "task" } : { ui };
    });
  } catch {
    /* older daemon without the ui service */
  }
}

export async function refreshTier() {
  const [w, m] = await Promise.allSettled([API.whoami(), API.me()]);
  if (w.status === "fulfilled") setState({ tier: w.value.tier === "desktop" ? "desktop" : w.value.tier === "viewer" ? "viewer" : "mobile" });
  if (m.status === "fulfilled") setState({ me: m.value });
}

/** Whether this person may approve an action of category. */
export function canApprove(me: Me | null, category: string): boolean {
  return !me || me.role === "owner" || me.approve.includes(category);
}

/** Phones (and other mobile-tier devices) may not manage things. */
export const canManage = (s: State = state) => s.tier === "desktop";

export async function resync() {
  await refreshTier();
  await Promise.allSettled([
    refreshAgents(),
    refreshSessions(),
    refreshApprovals(),
    refreshModels(),
    refreshSummaries(),
    refreshChannels(),
    refreshProfile(),
    refreshUI(),
    refreshTeams(),
    refreshAddonUpdates(),
    refreshAgentDocs(),
  ]);
  // first run on a phone: the list is empty, the welcome steps matter
  if (narrow() && state.listOpen && Object.keys(state.agents).length === 0) setState({ listOpen: false });
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
  // an agent opened a preview in the conversation on screen: show it
  if (ev.type === "preview/opened" && ev.data?.url && ev.session === state.current) openPreview(ev.session, { ...ev.data, agent: ev.data.agent || ev.agent });
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

let es: { close(): void } | null = null;
let retry = 0;
let retryTimer: number | undefined;

export function connectEvents() {
  if (es) es.close();
  window.clearTimeout(retryTimer);
  const tok = getToken();
  if (!tok) return;
  const handlers = new Map<string, (d: any) => void>();
  const on = (type: string, fn: (d: any) => void) => handlers.set(type, fn);
  const dispatch = (type: string, data: string) => {
    try {
      handlers.get(type)?.(JSON.parse(data));
    } catch {
      /* ignore malformed */
    }
  };
  let src: EventSource | null = null;
  if (viaRelay()) {
    // EventSource cannot go through the tunnel: read the stream ourselves
    const stop = tunnelEvents("/api/events", tok, dispatch, () => {
      setState({ connected: false });
      retry = Math.min(retry + 1, 6);
      retryTimer = window.setTimeout(() => getToken() && connectEvents(), 500 * 2 ** retry);
    });
    es = { close: stop };
  } else {
    src = new EventSource(`/api/events?token=${encodeURIComponent(tok)}`);
    es = src;
  }
  on("hello", () => {
    retry = 0;
    setState({ connected: true });
    void resync();
  });
  on("session.event", (d: SessionEvent) => applySessionEvent(d));
  on("agent.status", (d: { agent: string; status: AgentView["status"]; detail?: string }) => {
    const before = state.agents[d.agent];
    setState((s) => {
      const a = s.agents[d.agent];
      if (!a) return {};
      return { agents: { ...s.agents, [d.agent]: { ...a, status: d.status, detail: d.detail } } };
    });
    // the daemon drops events for a client that falls behind (a long
    // streamed reply): when an agent settles, read it again so its queue
    // is not left showing work that is done
    if (before && d.status !== "running" && before.queue.length > 0)
      void API.agent(d.agent).then(
        (a) => setState((s) => ({ agents: { ...s.agents, [a.id]: a } })),
        () => {},
      );
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
  on("computer.state", (d: ComputerState) => setState({ computer: d }));
  on("mcp", () => setState((s) => ({ mcpTick: s.mcpTick + 1 })));
  on("addons.updates", (d: { count: number }) => setState({ addonUpdates: d.count ?? 0 }));
  on("models.changed", () => void refreshModels());
  on("ui.changed", () => void refreshUI());
  on("members.changed", () => void refreshTier());
  on("teams.changed", () => void refreshTeams());
  on("board.changed", (d: { root: string }) => setState((s) => ({ boardTick: { ...s.boardTick, [d.root]: (s.boardTick[d.root] ?? 0) + 1 } })));
  if (!src) return;
  const es2 = src;
  for (const type of handlers.keys()) es2.addEventListener(type, (m) => dispatch(type, (m as MessageEvent).data));
  es2.onerror = () => {
    setState({ connected: false });
    if (es2.readyState === EventSource.CLOSED) {
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
