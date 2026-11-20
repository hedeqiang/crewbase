import { lazy, Suspense, useEffect, useState } from "react";
import { API, desktop, initToken, openExternal, setUnauthorizedHandler, setToken } from "./api";
import { t } from "./i18n";
import { applyTheme, connectEvents, disconnectEvents, getState, resync, sessionAgent, setState, useStore, applyHashRoute } from "./store";
import { actionOf } from "./shortcuts";
import { runShortcut } from "./actions";
import { ConvHeader, useNeedsYou } from "./components/Header";
import { SessionList } from "./components/SessionList";
import { Conversation } from "./components/Conversation";
import { Panel } from "./components/Panel";
import { LibraryPage } from "./components/Library";
import { NewAgent, emptyDraft, type Draft } from "./components/NewAgent";
import { Palette } from "./components/Palette";
import { TokenScreen } from "./components/TokenScreen";
import { PairScreen, pairParams } from "./components/PairScreen";
import { savedRelay, useRelay } from "./tunnel";
import { NewChannel } from "./components/NewChannel";
import { ConfirmHost } from "./components/Confirm";
import { UpdateNotes } from "./components/Update";

// Loaded when first opened: most sessions never visit settings.
const Settings = lazy(() => import("./components/Settings").then((m) => ({ default: m.Settings })));
const AddProviderDialog = lazy(() => import("./components/settings/AddProvider").then((m) => ({ default: m.AddProviderDialog })));

export function App() {
  const authed = useStore((s) => s.authed);
  const lang = useStore((s) => s.lang);

  useEffect(() => {
    applyTheme(getState().theme);
    setUnauthorizedHandler(() => {
      disconnectEvents();
      setToken(null);
      setState({ authed: false, connected: false });
    });
    // opened from a relay before: every call goes through the tunnel
    const rl = savedRelay();
    if (rl && !desktop()) useRelay(rl);
    if (pairParams()) return; // the pairing screen takes over
    const tok = initToken();
    if (!tok) {
      setState({ authed: false });
      return;
    }
    API.agents()
      .then(() => start())
      .catch((e) => {
        // 401 is handled above; network errors: keep trying via SSE reconnects.
        if (e?.status !== 401) start();
      });
  }, []);

  const [pairing, setPairing] = useState(() => pairParams());
  if (pairing)
    return (
      <PairScreen
        key={lang}
        code={pairing.code}
        relay={pairing.relay}
        onPaired={() => {
          setPairing(null);
          start();
        }}
      />
    );
  if (authed === null) return <div className="boot" />;
  if (!authed) return <TokenScreen key={lang} onAuthed={start} />;
  return <Shell key={lang} />;
}

function start() {
  setState({ authed: true });
  // Follow a deep link the desktop shell put in the URL, once data is in.
  void resync().then(() => applyHashRoute());
  connectEvents();
}

function Shell() {
  const view = useStore((s) => s.view);
  const listOpen = useStore((s) => s.listOpen);
  const panelOpen = useStore((s) => s.panelOpen);
  const connected = useStore((s) => s.connected);
  const current = useStore((s) => s.current);
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const channels = useStore((s) => s.channels);
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const hasAgent = !!sessionAgent(current) && (agents || sessions || channels);
  const showPanel = view === "chat" && panelOpen && hasAgent;
  const showList = listOpen && view !== "settings";
  const { n: needs } = useNeedsYou();

  // Desktop shell deep links (tray items, notifications, ⌘,).
  useEffect(() => {
    const on = () => applyHashRoute();
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  // Desktop shell: dock / taskbar badge = things waiting for the person.
  useEffect(() => {
    try {
      desktop()?.setBadge?.(needs);
    } catch {
      /* shell without badge support */
    }
  }, [needs]);

  // Desktop shell: open external links in the system browser.
  useEffect(() => {
    if (!desktop()?.openExternal) return;
    const click = (e: MouseEvent) => {
      const a = (e.target as HTMLElement | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
      if (!a || a.target !== "_blank" || !/^https?:/.test(a.href)) return;
      e.preventDefault();
      openExternal(a.href);
    };
    document.addEventListener("click", click);
    return () => document.removeEventListener("click", click);
  }, []);

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.isComposing) return;
      const el = e.target as HTMLElement | null;
      const typing = !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable);
      // while a shortcut is being recorded in settings, keys belong to it
      if (el?.closest?.("[data-recording]")) return;
      if (e.key === "Tab") {
        // focus stays in the topmost dialog
        const dlgs = Array.from(document.querySelectorAll<HTMLElement>(".modal, [role=alertdialog], [role=dialog][aria-modal=true]"));
        const dlg = dlgs[dlgs.length - 1];
        if (!dlg) return;
        const f = Array.from(dlg.querySelectorAll<HTMLElement>('button:not([disabled]), [href], input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')).filter((x) => x.offsetParent !== null);
        if (f.length === 0) return;
        const i = f.indexOf(document.activeElement as HTMLElement);
        if (i === -1 || (e.shiftKey && i === 0) || (!e.shiftKey && i === f.length - 1)) {
          e.preventDefault();
          (e.shiftKey ? f[f.length - 1] : f[0]).focus();
        }
        return;
      }
      if (e.key === "Escape") {
        // an open dialog closes first: Esc does what clicking its backdrop
        // does (the topmost one; dialogs with their own Esc handling have
        // already handled it, see defaultPrevented above)
        const scrims = Array.from(document.querySelectorAll<HTMLElement>(".scrim, .ovscrim.show")).filter((x) => !x.classList.contains("list-scrim") && !x.classList.contains("panel-scrim"));
        const top = scrims[scrims.length - 1];
        if (top) {
          // after the dialog's own handlers (later listeners on window): one
          // that handled Esc marks it, and a dialog that closed is gone
          setTimeout(() => {
            if (e.defaultPrevented || !top.isConnected) return;
            top.dispatchEvent(new MouseEvent(top.classList.contains("ovscrim") ? "mousedown" : "click", { bubbles: true }));
          }, 0);
          return;
        }
        const s = getState();
        if (s.palette) setState({ palette: false });
        else if (s.newChannel) setState({ newChannel: false });
        // the new-agent page is a step aside: Esc goes back (the draft stays)
        else if (s.view === "newAgent" && !typing) setState({ view: "chat" });
        else if (s.hostConfirm) setState({ hostConfirm: null });
        else if (window.matchMedia("(max-width: 820px)").matches && s.listOpen) setState({ listOpen: false });
        else if (window.matchMedia("(max-width: 1100px)").matches && s.panelOpen) setState({ panelOpen: false });
        return;
      }
      const id = actionOf(e, typing);
      if (!id) return;
      e.preventDefault();
      runShortcut(id);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);

  // Close overlays when shrinking into the overlay breakpoints.
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 1100px)");
    const on = () => mq.matches && setState({ panelOpen: false });
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);

  const cls = ["inner", showList ? "" : "nolist", showPanel ? "" : "nopanel"].filter(Boolean).join(" ");
  return (
    <div className="app">
      {!connected && <div className="offline">{t("conn.offline")}</div>}
      <div className={cls}>
        {view === "settings" ? (
          <Suspense fallback={<div className="center" />}>
            <Settings />
          </Suspense>
        ) : (
          <>
            {showList && <SessionList />}
            {showList && <div className="ovscrim list-scrim" onClick={() => setState({ listOpen: false })} />}
            {view === "library" ? (
              <LibraryPage />
            ) : (
              <div className="center">
                <ConvHeader draftColor={draft.color} draftShape={draft.shape} draftFace={draft.face} />
                {view === "newAgent" ? (
                  <NewAgent draft={draft} setDraft={setDraft} />
                ) : (
                  <Conversation onTemplate={(d) => setDraft({ ...emptyDraft(), ...d })} />
                )}
              </div>
            )}
            {showPanel && <Panel />}
            {showPanel && <div className="ovscrim panel-scrim" onClick={() => setState({ panelOpen: false })} />}
          </>
        )}
      </div>
      <Palette />
      <ConnectModel />
      <NewChannel />
      <Toasts />
      <UpdateNotes />
      <ConfirmHost />
    </div>
  );
}

function Toasts() {
  const toasts = useStore((s) => s.toasts);
  return (
    <div className="toasts" aria-live="polite">
      {toasts.map((x) => (
        <div key={x.id} className="toast">
          {x.text}
        </div>
      ))}
    </div>
  );
}

/** 连接模型, opened from wherever a missing model is noticed. */
function ConnectModel() {
  const on = useStore((s) => s.connecting);
  const providers = useStore((s) => s.models?.providers);
  if (!on) return null;
  return (
    <Suspense fallback={null}>
      <AddProviderDialog existing={(providers ?? []).map((p) => p.id)} onClose={() => setState({ connecting: false })} />
    </Suspense>
  );
}
