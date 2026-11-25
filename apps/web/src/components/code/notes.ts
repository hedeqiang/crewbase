// Comments on lines of the agent's changes, gathered before they go to the
// agent as one message. Kept per agent while the page lives (switching
// tabs keeps them).

import { useSyncExternalStore } from "react";
import type { Side } from "./diff";

export interface Note {
  id: string;
  path: string;
  line: number;
  side: Side;
  /** the code on that line */
  code: string;
  body: string;
}

const lists = new Map<string, Note[]>();
const subs = new Set<() => void>();
const ping = () => subs.forEach((f) => f());
const NONE: Note[] = [];
let seq = 0;

export interface Notes {
  list: Note[];
  add: (n: Omit<Note, "id">) => void;
  drop: (id: string) => void;
  clear: () => void;
}

export function useNotes(key: string): Notes {
  const list = useSyncExternalStore(
    (f) => (subs.add(f), () => void subs.delete(f)),
    () => lists.get(key) ?? NONE,
  );
  return {
    list,
    add: (n) => (lists.set(key, [...(lists.get(key) ?? []), { ...n, id: "n" + ++seq }]), ping()),
    drop: (id) => (lists.set(key, (lists.get(key) ?? []).filter((x) => x.id !== id)), ping()),
    clear: () => (lists.delete(key), ping()),
  };
}

/** The message the notes become: each with its file, line and code. */
export function notesMessage(notes: Note[], intro: string, lineWord: (path: string, line: number, side: Side) => string): string {
  const parts = notes.map((n, i) => {
    const code = n.code.trim() ? `\n   \`${n.code.trim().slice(0, 160).replace(/`/g, "'")}\`` : "";
    return `${i + 1}. ${lineWord(n.path, n.line, n.side)}${code}\n   ${n.body.replace(/\n/g, "\n   ")}`;
  });
  return `${intro}\n\n${parts.join("\n\n")}`;
}
