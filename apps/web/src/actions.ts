// What each shortcut does (shortcuts.ts holds the bindings).

import { leaveTo } from "./router";
import { API } from "./api";
import type { ActionId } from "./shortcuts";
import { getState, goHistory, newChat, openNewAgent, openNewChannel, openSession, openSettings, sessionAgent, setState, setTheme, toast } from "./store";
import { t } from "./i18n";

/** Sessions in the order the sidebar lists them (data-session on rows). */
function listedSessions(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>(".list .ci[data-session]")).map((el) => el.dataset.session!);
}

function focusSel(sel: string) {
  const el = document.querySelector<HTMLElement>(sel);
  if (el) {
    el.focus();
    if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) el.select?.();
  }
  return !!el;
}

export function runShortcut(id: ActionId) {
  const s = getState();
  switch (id) {
    case "palette":
      setState({ palette: !s.palette });
      break;
    case "newChat":
      void newChat();
      break;
    case "newAgent":
      openNewAgent();
      break;
    case "newChannel":
      openNewChannel();
      break;
    case "settings":
      openSettings();
      break;
    case "shortcuts":
      openSettings("keys");
      break;
    case "approvals":
      openSettings("approvals");
      break;
    case "theme": {
      const dark = document.documentElement.dataset.theme === "dark" || (!document.documentElement.dataset.theme && matchMedia("(prefers-color-scheme: dark)").matches);
      setTheme(dark ? "light" : "dark");
      break;
    }
    case "toggleList":
      setState({ listOpen: !s.listOpen });
      break;
    case "togglePanel":
      setState({ panelOpen: !s.panelOpen });
      break;
    case "search":
      if (s.view === "settings") leaveTo("chat");
      setState({ listOpen: true });
      setTimeout(() => focusSel(".list .search input"), 0);
      break;
    case "focusComposer":
      focusSel(".composer textarea, form textarea");
      break;
    case "back":
      goHistory(-1);
      break;
    case "forward":
      goHistory(1);
      break;
    case "prevChat":
    case "nextChat": {
      const list = listedSessions();
      if (!list.length) break;
      const i = s.current ? list.indexOf(s.current) : -1;
      const j = id === "nextChat" ? Math.min(list.length - 1, i + 1) : i < 0 ? 0 : Math.max(0, i - 1);
      openSession(list[j]);
      break;
    }
    case "stop": {
      const a = sessionAgent(s.current);
      if (a && (a.status === "running" || a.status === "waiting")) {
        API.action(a.id, "stop").then(
          () => toast(t("keys.stopped", { name: a.name })),
          (e) => toast(e?.message ?? String(e)),
        );
      }
      break;
    }
    case "tabTask":
    case "tabFiles":
    case "tabAgent":
      setState({ panelOpen: true, panelTab: id === "tabTask" ? "task" : id === "tabFiles" ? "files" : "agent" });
      break;
  }
}
