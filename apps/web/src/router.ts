// The address is where you are. Every place in the app has one (a hash,
// so the desktop shell, relays and plain browsers need nothing from the
// server):
//
//   #/c/<session>[?panel=<tab>&file=<path>&doc=<url>&sub=<session>]
//   #/settings/<section>
//   #/desk/<library|auto|memory>[?item=<library id>]   (工作台)
//   #/new-agent
//   #/                       (nothing open)
//
// The store still holds the state and the app still navigates by setting
// it (openSession, openSettings, setState({ panelTab: … }) …): the router
// watches those fields, records a history entry when the place changes
// (and only replaces the entry when the right panel merely opens or
// closes), and on back / forward puts the store back where the address
// says. So back, forward, ⌘[ ⌘], the mouse's side buttons, a trackpad
// swipe, the phone's back gesture, reloading and deep links all work the
// same way. Back first closes a dialog or an overlay that is open, like a
// native app.
//
// Older links keep working: #/session/<id>, #/settings/<section>[/<tab>],
// #/library, and #/settings/library|auto (now in the 工作台).

import { getState, loadSession, setState, subscribe, toast, type DeskPage, type PanelTab, type SettingsSection, type State } from "./store";
import { t } from "./i18n";

export interface Loc {
  view: "chat" | "settings" | "newAgent" | "desk";
  session?: string;
  panel?: PanelTab; // the right panel is open on this tab
  file?: string;
  doc?: string;
  sub?: string;
  section?: SettingsSection;
  desk?: DeskPage;
  tab?: string; // a settings page's own tab (older links)
  item?: number;
}

const PANEL_TABS = new Set(["task", "schedule", "files", "code", "preview", "library", "docs", "agent"]);

/** The place the store shows. */
const sheetPanel = () => window.matchMedia("(max-width: 820px)").matches;

export function locOf(s: State): Loc {
  if (s.view === "settings") return { view: "settings", section: s.settingsList && sheetPanel() ? undefined : s.settings };
  if (s.view === "desk") return { view: "desk", desk: s.desk, item: s.desk === "library" && s.libOpen !== null ? s.libOpen : undefined };
  if (s.view === "newAgent") return { view: "newAgent" };
  const l: Loc = { view: "chat", session: s.current ?? undefined };
  if (s.current && s.panelOpen) {
    l.panel = s.panelTab;
    if (s.panelTab === "files" && s.fileOpen) l.file = s.fileOpen;
    if (s.panelTab === "docs" && s.docsOpen) l.doc = s.docsOpen;
    if (s.panelTab === "task" && s.subView) l.sub = s.subView;
  }
  return l;
}

export function format(l: Loc): string {
  const q = new URLSearchParams();
  let path = "#/";
  if (l.view === "settings") {
    path = l.section ? "#/settings/" + encodeURIComponent(l.section) : "#/settings";
  } else if (l.view === "desk") {
    path = "#/desk/" + (l.desk ?? "library");
    if (l.item !== undefined) q.set("item", String(l.item));
  } else if (l.view === "newAgent") {
    path = "#/new-agent";
  } else if (l.session) {
    path = "#/c/" + encodeURIComponent(l.session);
    if (l.panel) q.set("panel", l.panel);
    if (l.file) q.set("file", l.file);
    if (l.doc) q.set("doc", l.doc);
    if (l.sub) q.set("sub", l.sub);
  }
  const qs = q.toString();
  return qs ? path + "?" + qs : path;
}

/** A hash as a place (null: not one of ours, e.g. #/pair?…). */
export function parse(hash: string): Loc | null {
  const h = hash.replace(/^#/, "") || "/";
  const [path, qs = ""] = h.split("?", 2);
  const q = new URLSearchParams(qs);
  const parts = path.split("/").filter(Boolean).map((x) => {
    try {
      return decodeURIComponent(x);
    } catch {
      return x;
    }
  });
  if (parts.length === 0) return { view: "chat" };
  switch (parts[0]) {
    case "c":
    case "session": {
      if (!parts[1]) return { view: "chat" };
      const l: Loc = { view: "chat", session: parts[1] };
      const p = q.get("panel");
      if (p && (PANEL_TABS.has(p) || p.startsWith("ui:"))) l.panel = p as PanelTab;
      l.file = q.get("file") ?? undefined;
      l.doc = q.get("doc") ?? undefined;
      l.sub = q.get("sub") ?? undefined;
      return l;
    }
    case "settings": {
      const item = q.get("item");
      // these moved to the 工作台
      if (parts[1] && isDesk(parts[1])) return { view: "desk", desk: parts[1], item: item && /^\d+$/.test(item) ? Number(item) : undefined };
      return { view: "settings", section: (parts[1] || undefined) as SettingsSection | undefined, tab: parts[2] };
    }
    case "desk": {
      const item = q.get("item");
      return { view: "desk", desk: parts[1] && isDesk(parts[1]) ? parts[1] : "library", item: item && /^\d+$/.test(item) ? Number(item) : undefined };
    }
    case "library": {
      const id = parts[1] && /^\d+$/.test(parts[1]) ? Number(parts[1]) : undefined;
      return { view: "desk", desk: "library", item: id };
    }
    case "new-agent":
      return { view: "newAgent" };
  }
  return null;
}

const isDesk = (x: string): x is DeskPage => x === "library" || x === "auto" || x === "memory";

let started = false;
let applying = false;
let queued = false;
let shown = ""; // the hash the store was last brought to or written as
// this tab's entries as the router made them (history.state.crewbase is
// the index), so a 返回 button can go back to the place it names
let entries: string[] = [];
let idx = 0;

const here = () => location.pathname + location.search;

/** Whether going from a to b is a new place (a history entry) rather than
 *  the same place with the panel opened or closed. */
function isNewPlace(a: Loc | null, b: Loc): boolean {
  if (!a) return false;
  if (a.view !== b.view || a.session !== b.session || a.section !== b.section || a.desk !== b.desk || a.item !== b.item) return true;
  // the panel opened or closed: the same place, except on a phone-width
  // window, where the panel is a sheet over it (back closes it)
  if (!a.panel || !b.panel) return !a.panel !== !b.panel && sheetPanel();
  return a.panel !== b.panel || a.file !== b.file || a.doc !== b.doc || a.sub !== b.sub;
}

/** Store → address (after the store settles in this tick). */
function sync() {
  if (!started || applying || queued) return;
  queued = true;
  queueMicrotask(() => {
    queued = false;
    if (applying) return;
    const next = format(locOf(getState()));
    if (next === shown) return;
    const prev = parse(shown);
    const push = isNewPlace(prev, parse(next)!);
    // back where the previous entry was (a sheet closed with its ✕): step
    // back instead of adding one
    if (push && idx > 0 && entries[idx - 1] === next) {
      idx--;
      shown = next;
      history.back();
      return;
    }
    if (push) {
      idx++;
      entries.length = idx;
    }
    entries[idx] = next;
    history[push ? "pushState" : "replaceState"]({ crewbase: idx }, "", here() + next);
    shown = next;
  });
}

/** Address → store. */
export function apply(l: Loc) {
  const s = getState();
  applying = true;
  try {
    if (l.view === "settings") {
      setState({ view: "settings", settings: l.section ?? s.settings ?? "general", settingsList: !l.section && sheetPanel(), palette: false, ...(l.tab ? { settingsTab: l.tab } : {}) });
    } else if (l.view === "desk") {
      setState({ view: "desk", desk: l.desk ?? "library", palette: false, ...(l.desk === "library" ? { libOpen: l.item ?? null } : {}), ...(sheetPanel() ? { listOpen: false } : {}) });
    } else if (l.view === "newAgent") {
      setState({ view: "newAgent", palette: false, ...(sheetPanel() ? { listOpen: false } : {}) });
    } else {
      const session = l.session ?? null;
      const known = !session || !!s.sessions[session] || !!s.channels[session] || Object.values(s.agents).some((a) => a.session === session);
      setState({
        view: "chat",
        current: known ? session : s.current,
        palette: false,
        // on a phone the list gives way to the conversation, as a tap on it
        // does, and comes back when there is no conversation (back to #/)
        ...(sheetPanel() ? { listOpen: !session } : {}),
        panelOpen: !!l.panel,
        ...(l.panel ? { panelTab: l.panel } : {}),
        fileOpen: l.file ?? null,
        docsOpen: l.doc ?? null,
        subView: l.sub ?? null,
      });
      if (session && known && session !== s.current) void loadSession(session);
      // an old link or a deleted conversation: say so instead of a blank page
      if (session && !known && s.sessionsLoaded) toast(t("route.gone"));
    }
  } finally {
    applying = false;
  }
  shown = format(locOf(getState()));
  entries[idx] = shown;
  // the address names what is shown (a gone conversation, an older link)
  if (location.hash !== shown) history.replaceState({ crewbase: idx }, "", here() + shown);
}

/** Closes the topmost dialog or overlay; false when none is open. */
export type OverlayCloser = () => boolean;
let closeOverlay: OverlayCloser = () => false;
export function setOverlayCloser(f: OverlayCloser) {
  closeOverlay = f;
}

function onPop(e: Event) {
  const l = parse(location.hash);
  if (!l) return;
  if (location.hash === shown) return;
  const at = (history.state as { crewbase?: number } | null)?.crewbase;
  // back / forward reach one of our entries; a hash set from outside (the
  // tray, a notification, a typed address) is a new entry with no state.
  // Only back closes what is open on top instead of moving.
  if (typeof at === "number" && at < idx && closeOverlay()) {
    idx++;
    entries.length = idx;
    entries[idx] = shown;
    history.pushState({ crewbase: idx }, "", here() + shown);
    return;
  }
  void e;
  if (typeof at === "number") idx = at;
  else {
    // a hash set from outside (tray, notification): a new entry
    idx++;
    entries.length = idx;
    history.replaceState({ crewbase: idx }, "", location.href);
  }
  apply(l);
}

/** Goes back to the nearest earlier place that is where (in this tab's
 *  history), else does fallback: a 返回 button returns where you came from
 *  instead of adding a step. */
export function backTo(where: (l: Loc) => boolean, fallback: () => void) {
  for (let j = idx - 1; j >= 0; j--) {
    const l = entries[j] !== undefined ? parse(entries[j]) : null;
    if (l && where(l)) {
      history.go(j - idx);
      return;
    }
  }
  fallback();
}

/** Starts following the address (after the first data is in). */
export function startRouter() {
  if (started) return;
  started = true;
  subscribe(sync);
  window.addEventListener("popstate", onPop);
  // the desktop shell and older code set location.hash (tray, notifications)
  window.addEventListener("hashchange", onPop);
  const at = (history.state as { crewbase?: number } | null)?.crewbase;
  idx = typeof at === "number" ? at : 0;
  entries = [];
  const l = parse(location.hash);
  if (l && location.hash && location.hash !== "#/" && location.hash !== "#") {
    apply(l);
    history.replaceState({ crewbase: idx }, "", here() + shown); // this entry is ours
  } else {
    shown = format(locOf(getState()));
    entries[idx] = shown;
    history.replaceState({ crewbase: idx }, "", here() + shown);
  }
  setState({ routed: true });
  // the mouse's back and forward buttons (browsers that do not map them)
  window.addEventListener("mouseup", (e) => {
    if (e.button === 3 || e.button === 4) {
      e.preventDefault();
      history.go(e.button === 3 ? -1 : 1);
    }
  });
}

/** ⌘[ / ⌘]: the app's history is the browser's. */
export function go(dir: -1 | 1) {
  history.go(dir);
}

/** Leaves the page shown for a view (settings, the new-agent page → chat):
 *  back to it when it is in this tab's history, else straight there. */
export function leaveTo(view: Loc["view"]) {
  backTo(
    (l) => l.view === view,
    () => setState({ view }),
  );
}
