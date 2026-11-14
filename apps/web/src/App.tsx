import { useEffect, useState } from "react";
import { API, initToken, setUnauthorizedHandler, setToken } from "./api";
import { t } from "./i18n";
import { applyTheme, connectEvents, disconnectEvents, getState, openNewAgent, resync, sessionAgent, setState, useStore } from "./store";
import { Titlebar } from "./components/Titlebar";
import { SessionList } from "./components/SessionList";
import { Conversation } from "./components/Conversation";
import { Panel } from "./components/Panel";
import { NewAgent, emptyDraft, type Draft } from "./components/NewAgent";
import { Settings } from "./components/Settings";
import { Palette } from "./components/Palette";
import { TokenScreen } from "./components/TokenScreen";

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

  if (authed === null) return <div className="boot" />;
  if (!authed) return <TokenScreen key={lang} onAuthed={start} />;
  return <Shell key={lang} />;
}

function start() {
  setState({ authed: true });
  void resync();
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
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const hasAgent = !!sessionAgent(current) && (agents || sessions);
  const showPanel = view === "chat" && panelOpen && hasAgent;
  const showList = listOpen && view !== "settings";

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setState((s) => ({ palette: !s.palette }));
      } else if (mod && e.key.toLowerCase() === "n" && !e.shiftKey) {
        e.preventDefault();
        openNewAgent();
      } else if (e.key === "Escape") {
        const s = getState();
        if (s.palette) setState({ palette: false });
        else if (s.hostConfirm) setState({ hostConfirm: null });
        else if (window.matchMedia("(max-width: 820px)").matches && s.listOpen) setState({ listOpen: false });
        else if (window.matchMedia("(max-width: 1100px)").matches && s.panelOpen) setState({ panelOpen: false });
      }
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
      <Titlebar draftColor={draft.color} draftShape={draft.shape} />
      {!connected && <div className="offline">{t("conn.offline")}</div>}
      <div className={cls}>
        {view === "settings" ? (
          <Settings />
        ) : (
          <>
            {showList && <SessionList />}
            {showList && <div className="ovscrim list-scrim" onClick={() => setState({ listOpen: false })} />}
            {view === "newAgent" ? <NewAgent draft={draft} setDraft={setDraft} /> : <Conversation />}
            {showPanel && <Panel />}
            {showPanel && <div className="ovscrim panel-scrim" onClick={() => setState({ panelOpen: false })} />}
          </>
        )}
      </div>
      <Palette />
      <Toasts />
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
