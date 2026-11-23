import { type AgentItem, type FeedItem, type Rewind } from "../../projection";

/** How far from the bottom each conversation was left (only when not at
 *  the bottom), so going back to it returns to the same place. */
export const scrollMemo = new Map<string, number>();

/** The leading turn of a run: its user messages and what answered them,
 *  up to the next user message after a reply. */
function leadTurn(items: FeedItem[]): number {
  let replied = false;
  for (let i = 0; i < items.length; i++) {
    const k = items[i].k;
    if (k === "user" && replied) return i;
    if (k === "agent" || k === "turn") replied = true;
  }
  return items.length;
}

/** Messages and turns of a conversation rendered at first (and added per
 *  显示更早的). */
export const FEED_WINDOW = 80;

/** Segments as rendered: runs of regenerated answers followed by the one
 *  the model sees become versions; other runs stay as they are. */
type Block = { kind: "rows"; items: FeedItem[] } | { kind: "sup"; rw: Rewind; items: FeedItem[] } | { kind: "versions"; older: FeedItem[][]; current: FeedItem[] };
export function blocks(items: FeedItem[]): Block[] {
  const segs = segments(items);
  const out: Block[] = [];
  for (let i = 0; i < segs.length; i++) {
    const s = segs[i];
    if (s.sup?.reason === "regenerate") {
      const older: FeedItem[][] = [];
      let j = i;
      while (j < segs.length && segs[j].sup?.reason === "regenerate") older.push(segs[j++].items);
      const next = segs[j];
      if (next && !next.sup && older.every((o) => o.some((x) => x.k === "agent" || x.k === "turn"))) {
        const n = leadTurn(next.items);
        out.push({ kind: "versions", older, current: next.items.slice(0, n) });
        if (n < next.items.length) out.push({ kind: "rows", items: next.items.slice(n) });
        i = j;
        continue;
      }
    }
    out.push(s.sup ? { kind: "sup", rw: s.sup, items: s.items } : { kind: "rows", items: s.items });
  }
  return out;
}

/** The feed split into runs the model still sees and runs it does not. */
function segments(items: FeedItem[]): { sup?: Rewind; items: FeedItem[] }[] {
  const out: { sup?: Rewind; items: FeedItem[] }[] = [];
  for (const it of items) {
    const last = out[out.length - 1];
    if (last && last.sup?.at === it.superseded?.at) last.items.push(it);
    else out.push({ sup: it.superseded, items: [it] });
  }
  return out;
}

const SYS_KINDS = new Set<FeedItem["k"]>(["switch", "mode", "compact", "handover", "owner", "chstate", "sys"]);

function isSysItem(it: FeedItem): boolean {
  return SYS_KINDS.has(it.k) || (it.k === "approval" && !!it.decided);
}

/** Split the feed into runs; consecutive system lines become one compact group.
 *  Agent messages show their model only when it differs from the previous one. */
type Row = { it: FeedItem; showModel?: boolean };

export function groupFeed(items: FeedItem[]): { sys: boolean; items: Row[] }[] {
  const out: { sys: boolean; items: Row[] }[] = [];
  let prev = "";
  for (const it of items) {
    const sys = isSysItem(it);
    let showModel: boolean | undefined;
    const m = it.k === "turn" ? lastMsg(it)?.model : it.k === "agent" ? it.model : undefined;
    if (m) {
      const key = m.provider + "/" + m.model + "@" + (m.effort ?? "");
      showModel = key !== prev;
      prev = key;
    }
    const last = out[out.length - 1];
    if (last && sys && last.sys) last.items.push({ it });
    else out.push({ sys, items: [{ it, showModel }] });
  }
  return out;
}

export const lastMsg = (t: Extract<FeedItem, { k: "turn" }>): AgentItem | undefined => {
  for (let i = t.parts.length - 1; i >= 0; i--) {
    const p = t.parts[i];
    if (p.k === "msg") return p.it;
  }
  return undefined;
};
