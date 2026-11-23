import { installExitGhosts } from "./lib/motion";
import { lazy, Suspense, useEffect, useState } from "react";
import { API, type AgentView, desktop, initToken, openExternal, setUnauthorizedHandler, setToken } from "./api";
import { t } from "./i18n";
import { applyTheme, connectEvents, disconnectEvents, docsEmbeddable, getState, isDocsLink, openDocs, resync, sessionAgent, setState, useStore } from "./store";
import { leaveTo, setOverlayCloser, startRouter } from "./router";
import { actionOf } from "./shortcuts";
import { runShortcut } from "./actions";
import { ConvHeader, useNeedsYou } from "./components/Header";
import { SessionList } from "./components/SessionList";
import { Conversation } from "./components/Conversation";
import { Panel } from "./components/Panel";
import { NewAgent, emptyDraft, type Draft } from "./components/NewAgent";
import { Palette } from "./components/Palette";
import { TokenScreen } from "./components/TokenScreen";
import { PairScreen, pairParams } from "./components/PairScreen";
import { savedRelay, useRelay } from "./tunnel";
import { NewChannel } from "./components/NewChannel";
import { ConfirmHost } from "./components/Confirm";
import { UpdateNotes } from "./components/Update";
import { Logo } from "./components/Logo";

// Loaded when first opened: most sessions never visit settings.
const Settings = lazy(() => import("./components/Settings").then((m) => ({ default: m.Settings })));
const Desk = lazy(() => import("./components/Desk").then((m) => ({ default: m.Desk })));
const AddProviderDialog = lazy(() => import("./components/settings/AddProvider").then((m) => ({ default: m.AddProviderDialog })));

export function App() {
  const authed = useStore((s) => s.authed);
  const lang = useStore((s) => s.lang);

  useEffect(() => {
    installExitGhosts();
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
      .then((r) => {
        // the list the check fetched is shown at once (no second wait)
        const agents: Record<string, AgentView> = {};
        for (const a of r.agents) agents[a.id] = a;
        setState({ agents, agentsLoaded: true });
        start();
      })
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
  if (authed === null)
    return (
      <div className="boot" role="status" aria-label={t("common.loading")}>
        <Logo size={40} />
        <i className="bootbar" />
      </div>
    );
  if (!authed) return <TokenScreen key={lang} onAuthed={start} />;
  return <Shell key={lang} />;
}

function start() {
  setState({ authed: true });
  // The address says where to be (a reload, a deep link): followed once
  // the first data is in, then kept in step (router.ts).
  void resync().then(() => startRouter());
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

  // Back first closes what is open on top (a dialog, ⌘K, an overlay), like
  // a native app: the same as Esc.
  useEffect(() => {
    setOverlayCloser(closeTopOverlay);
  }, []);

  // Desktop shell: dock / taskbar badge = things waiting for the person.
  useEffect(() => {
    try {
      desktop()?.setBadge?.(needs);
    } catch {
      /* shell without badge support */
    }
  }, [needs]);

  // Links to the connected docs open them in the right panel.
  useEffect(() => {
    const click = (e: MouseEvent) => {
      if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = (e.target as HTMLElement | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
      if (!a || !isDocsLink(a.href) || !docsEmbeddable()) return;
      e.preventDefault();
      openDocs(a.href);
    };
    document.addEventListener("click", click, true);
    return () => document.removeEventListener("click", click, true);
  }, []);

  // Desktop shell: open external links in the system browser.
  useEffect(() => {
    if (!desktop()?.openExternal) return;
    const click = (e: MouseEvent) => {
      if (e.defaultPrevented) return;
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
        const s = getState();
        // the new-agent page is a step aside: Esc goes back (the draft stays)
        if (!closeTopOverlay(e) && s.view === "newAgent" && !typing) leaveTo("chat");
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
    const mq = window.matchMedia("(max-width: 960px)");
    const on = () => mq.matches && setState({ panelOpen: false });
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);

  // on a phone with no conversation open the list is the page, not a drawer
  const listHome = showList && view === "chat" && !current;
  const cls = ["inner", showList ? "" : "nolist", showPanel ? "" : "nopanel", listHome ? "listhome" : ""].filter(Boolean).join(" ");
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
            {view === "desk" ? (
              <Suspense fallback={<div className="center" />}>
                <Desk />
              </Suspense>
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

/** Closes the topmost dialog or overlay, as Esc and back do; false when
 *  nothing is open on top. With e (an Esc), dialogs that handle Esc
 *  themselves have their say first. */
function closeTopOverlay(e?: KeyboardEvent): boolean {
  // a dialog: what clicking its backdrop does (the topmost one)
  const scrims = Array.from(document.querySelectorAll<HTMLElement>(".scrim, .ovscrim.show")).filter((x) => !x.classList.contains("list-scrim") && !x.classList.contains("panel-scrim"));
  const top = scrims[scrims.length - 1];
  if (top) {
    const close = () => top.dispatchEvent(new MouseEvent(top.classList.contains("ovscrim") ? "mousedown" : "click", { bubbles: true }));
    if (e) {
      // after the dialog's own handlers (later listeners on window): one
      // that handled Esc marks it, and a dialog that closed is gone
      setTimeout(() => {
        if (e.defaultPrevented || !top.isConnected) return;
        close();
      }, 0);
    } else close();
    return true;
  }
  const s = getState();
  if (s.palette) return setState({ palette: false }), true;
  if (s.newChannel) return setState({ newChannel: false }), true;
  if (s.hostConfirm) return setState({ hostConfirm: null }), true;
  // the list and the panel as overlays on a narrow window
  if (window.matchMedia("(max-width: 820px)").matches && s.listOpen) return setState({ listOpen: false }), true;
  // (the panel as a sheet on a phone is a step of its own: router.ts)
  if (window.matchMedia("(max-width: 1100px)").matches && !window.matchMedia("(max-width: 820px)").matches && s.panelOpen && s.view === "chat") return setState({ panelOpen: false }), true;
  return false;
}
