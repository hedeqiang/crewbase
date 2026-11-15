// Keyboard shortcuts: one registry for the global key handler, the
// settings page (设置 › 快捷键) and rebinding. A binding is a string like
// "mod+shift+n" ("mod" = ⌘ on macOS, Ctrl elsewhere); people can change
// any rebindable shortcut, and the changes are kept in this browser.
//
// Composer keys (Enter to send, Shift+Enter for a new line, @ to mention)
// are typing rules, listed but not rebindable.

import type { Key } from "./i18n";

export const MAC = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

export type ActionId =
  | "palette"
  | "newChat"
  | "newAgent"
  | "newChannel"
  | "settings"
  | "toggleList"
  | "togglePanel"
  | "search"
  | "focusComposer"
  | "back"
  | "forward"
  | "prevChat"
  | "nextChat"
  | "stop"
  | "tabTask"
  | "tabFiles"
  | "tabAgent"
  | "approvals"
  | "shortcuts"
  | "theme";

export interface Shortcut {
  id: ActionId;
  group: "general" | "nav" | "chat" | "panel";
  label: Key;
  def: string;
  // works while typing in a text field (otherwise ignored there)
  inInput?: boolean;
}

export const SHORTCUTS: Shortcut[] = [
  { id: "palette", group: "general", label: "keys.palette", def: "mod+k", inInput: true },
  { id: "newChat", group: "general", label: "keys.newChat", def: "mod+n", inInput: true },
  { id: "newAgent", group: "general", label: "keys.newAgent", def: "mod+shift+n", inInput: true },
  { id: "newChannel", group: "general", label: "keys.newChannel", def: "mod+alt+n", inInput: true },
  { id: "settings", group: "general", label: "keys.settings", def: "mod+,", inInput: true },
  { id: "shortcuts", group: "general", label: "keys.showShortcuts", def: "mod+/", inInput: true },
  { id: "theme", group: "general", label: "keys.theme", def: "mod+shift+l", inInput: true },
  { id: "toggleList", group: "nav", label: "keys.toggleList", def: "mod+\\", inInput: true },
  { id: "togglePanel", group: "nav", label: "keys.togglePanel", def: "mod+shift+\\", inInput: true },
  { id: "search", group: "nav", label: "keys.search", def: "mod+shift+f", inInput: true },
  { id: "back", group: "nav", label: "keys.back", def: "mod+[", inInput: true },
  { id: "forward", group: "nav", label: "keys.forward", def: "mod+]", inInput: true },
  { id: "prevChat", group: "nav", label: "keys.prevChat", def: "alt+arrowup", inInput: true },
  { id: "nextChat", group: "nav", label: "keys.nextChat", def: "alt+arrowdown", inInput: true },
  { id: "approvals", group: "nav", label: "keys.approvals", def: "mod+shift+a", inInput: true },
  { id: "focusComposer", group: "chat", label: "keys.focusComposer", def: "/" },
  { id: "stop", group: "chat", label: "keys.stop", def: "mod+.", inInput: true },
  { id: "tabTask", group: "panel", label: "keys.tabTask", def: "mod+1", inInput: true },
  { id: "tabFiles", group: "panel", label: "keys.tabFiles", def: "mod+2", inInput: true },
  { id: "tabAgent", group: "panel", label: "keys.tabAgent", def: "mod+3", inInput: true },
];

/** Typing rules shown with the shortcuts (not rebindable). */
export function fixedKeys(): { group: Shortcut["group"]; label: Key; keys: string }[] {
  const es = enterSends();
  return [
    { group: "chat", label: "keys.send", keys: es ? "enter" : "mod+enter" },
    { group: "chat", label: "keys.newline", keys: es ? "shift+enter" : "enter" },
    { group: "chat", label: "keys.mention", keys: "@" },
    { group: "general", label: "keys.close", keys: "escape" },
  ];
}

const ENTER_KEY = "crewbase.enterSends";

/** Whether plain Enter sends a message (default yes; Shift+Enter is a new
 *  line). Off, ⌘Enter sends and Enter is a new line. */
export function enterSends(): boolean {
  try {
    return localStorage.getItem(ENTER_KEY) !== "0";
  } catch {
    return true;
  }
}

export function setEnterSends(on: boolean) {
  try {
    localStorage.setItem(ENTER_KEY, on ? "1" : "0");
  } catch {
    /* private mode */
  }
  listeners.forEach((f) => f());
}

// Shortcuts the browser or the system keep for themselves.
const RESERVED = new Set(["mod+q", "mod+w", "mod+t", "mod+r", "mod+shift+r", "mod+c", "mod+v", "mod+x", "mod+z", "mod+shift+z", "mod+a", "mod+f", "mod+h", "mod+m", "mod+tab", "mod+l", "mod+p", "mod+s"]);

const LS_KEY = "crewbase.shortcuts";

let overrides: Partial<Record<ActionId, string>> = load();
const listeners = new Set<() => void>();

function load(): Partial<Record<ActionId, string>> {
  try {
    const v = JSON.parse(localStorage.getItem(LS_KEY) || "{}");
    return v && typeof v === "object" ? v : {};
  } catch {
    return {};
  }
}

function save() {
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(overrides));
  } catch {
    /* private mode */
  }
  listeners.forEach((f) => f());
}

export function onShortcutsChange(f: () => void): () => void {
  listeners.add(f);
  return () => listeners.delete(f);
}

/** The binding in effect ("" = turned off). */
export function binding(id: ActionId): string {
  const o = overrides[id];
  if (o !== undefined) return o;
  return SHORTCUTS.find((s) => s.id === id)?.def ?? "";
}

export function isCustom(id: ActionId): boolean {
  return overrides[id] !== undefined;
}

/** Why combo cannot be used for id ("" = it can). */
export function conflict(id: ActionId, combo: string): string {
  if (!combo) return "";
  if (RESERVED.has(combo)) return "reserved";
  const parts = combo.split("+");
  const key = parts[parts.length - 1];
  if (parts.length === 1 && key.length === 1 && id !== "focusComposer") return "needsModifier";
  const other = SHORTCUTS.find((s) => s.id !== id && binding(s.id) === combo);
  return other ? other.id : "";
}

export function setBinding(id: ActionId, combo: string) {
  const def = SHORTCUTS.find((s) => s.id === id)?.def;
  if (combo === def) delete overrides[id];
  else overrides[id] = combo;
  save();
}

export function resetBinding(id: ActionId) {
  delete overrides[id];
  save();
}

export function resetAll() {
  overrides = {};
  save();
}

const NAMED: Record<string, string> = {
  " ": "space",
  arrowup: "arrowup",
  arrowdown: "arrowdown",
  arrowleft: "arrowleft",
  arrowright: "arrowright",
  escape: "escape",
  enter: "enter",
  backspace: "backspace",
  delete: "delete",
  tab: "tab",
};

/** The combo of a key event ("" for a lone modifier). Letters and digits
 *  come from e.code, so layouts and Shift / Alt do not change them. */
export function comboOf(e: KeyboardEvent): string {
  const k = e.key.toLowerCase();
  if (["meta", "control", "shift", "alt", "os"].includes(k)) return "";
  let key = NAMED[k] ?? k;
  if (/^Key[A-Z]$/.test(e.code)) key = e.code.slice(3).toLowerCase();
  else if (/^Digit\d$/.test(e.code)) key = e.code.slice(5);
  else if (e.code === "Backslash") key = "\\";
  else if (e.code === "Slash") key = "/";
  else if (e.code === "Comma") key = ",";
  else if (e.code === "Period") key = ".";
  else if (e.code === "BracketLeft") key = "[";
  else if (e.code === "BracketRight") key = "]";
  const mod = MAC ? e.metaKey : e.ctrlKey;
  const parts: string[] = [];
  if (mod) parts.push("mod");
  if (MAC && e.ctrlKey) parts.push("ctrl");
  if (e.altKey) parts.push("alt");
  if (e.shiftKey) parts.push("shift");
  parts.push(key);
  return parts.join("+");
}

/** The action bound to e, if any. */
export function actionOf(e: KeyboardEvent, typing: boolean): ActionId | null {
  const c = comboOf(e);
  if (!c) return null;
  for (const s of SHORTCUTS) {
    if (binding(s.id) === c && (!typing || s.inInput)) return s.id;
  }
  return null;
}

const GLYPH_MAC: Record<string, string> = { mod: "⌘", ctrl: "⌃", alt: "⌥", shift: "⇧" };
const GLYPH_OTHER: Record<string, string> = { mod: "Ctrl", ctrl: "Ctrl", alt: "Alt", shift: "Shift" };
const KEYS: Record<string, string> = {
  arrowup: "↑",
  arrowdown: "↓",
  arrowleft: "←",
  arrowright: "→",
  escape: "Esc",
  enter: "Enter",
  space: "Space",
  backspace: "⌫",
  delete: "Del",
  tab: "Tab",
};

/** How a combo is shown: ⌘⇧N on macOS, Ctrl+Shift+N elsewhere. */
export function display(combo: string): string[] {
  if (!combo) return [];
  return combo.split("+").map((p) => {
    const g = (MAC ? GLYPH_MAC : GLYPH_OTHER)[p];
    if (g) return g;
    return KEYS[p] ?? (p.length === 1 ? p.toUpperCase() : p);
  });
}

/** The current binding of id as text (⌘⇧N / Ctrl+Shift+N), for menus and tooltips. */
export function keyLabel(id: ActionId): string {
  const d = display(binding(id));
  return MAC ? d.join("") : d.join("+");
}
