import { excerpt } from "../MsgExtras";
import { type AgentView } from "../../api";
import { t } from "../../i18n";
import { type FeedItem } from "../../projection";
import { getState } from "../../store";

/** A message by its seq, for the pinned bar: who and an excerpt. */
export function findMsg(items: FeedItem[], seq: number): { seq: number; who: string; text: string } | null {
  const agents = getState().agents;
  for (const it of items) {
    if (it.k === "user" && it.seq === seq) return { seq, who: it.from || t("rx.you"), text: excerpt(it.text, 80) };
    if (it.k === "agent" && Number(it.id) === seq) return { seq, who: agents[it.agent]?.name ?? it.agent, text: excerpt(it.text, 80) };
    if (it.k === "turn")
      for (const p of it.parts) if (p.k === "msg" && Number(p.it.id) === seq) return { seq, who: agents[it.agent]?.name ?? it.agent, text: excerpt(p.it.text, 80) };
  }
  return null;
}

export function siteOf(u: string): string {
  try {
    return new URL(u).hostname.replace(/^www\./, "");
  } catch {
    return u;
  }
}

/** How an agent is named in the conversation (a blank chat speaks as 助手). */
export function speaker(a: AgentView): string {
  return a.kind === "chat" ? t("chat.assistant") : a.name;
}

export function shortPath(p: string): string {
  const m = p.match(/^\/(Users|home)\/[^/]+(\/.*)?$/);
  return m ? "~" + (m[2] ?? "") : p;
}

/** Middle-ellipsis path: keeps the head and the last segment visible. */
/** Label of the working folder: the project's name, or 默认工作区. */
export function dirName(p: string, project: boolean): string {
  if (!project) return t("dir.default");
  const parts = p.replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || p;
}

const RECENT_KEY = "crewbase.recentDirs";

export function recentDirs(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(RECENT_KEY) || "[]");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string").slice(0, 6) : [];
  } catch {
    return [];
  }
}

export function rememberDir(d: string) {
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify([d, ...recentDirs().filter((x) => x !== d)].slice(0, 6)));
  } catch {
    /* private mode */
  }
}

/** The end of a long path: "…/code/shop". */
export function tailPath(p: string, n = 34): string {
  if (p.length <= n) return p;
  const parts = p.split("/");
  let out = parts.pop() ?? "";
  while (parts.length && out.length + parts[parts.length - 1].length + 1 <= n - 2) out = parts.pop() + "/" + out;
  return "…/" + out;
}

/** 思考了 6 秒 / 思考了 3 次 · 共 1 分 10 秒: how long it thought, when the
 *  log knows (the model calls that thought), else 思考过程. */
export function thinkLabel(ms: (number | undefined)[]): string {
  const known = ms.filter((x): x is number => typeof x === "number" && x > 0);
  // a total that leaves some thoughts out would understate it
  if (!known.length || known.length < ms.length) return ms.length > 1 ? t("chat.thoughtsN", { n: ms.length }) : t("chat.thoughts");
  const s = Math.max(1, Math.round(known.reduce((a, b) => a + b, 0) / 1000));
  const dur = s < 60 ? t("dur.s", { n: s }) : t("dur.ms", { m: Math.floor(s / 60), s: s % 60 });
  return ms.length > 1 ? t("chat.thoughtTimes", { n: ms.length, dur }) : t("chat.thoughtFor", { dur });
}
