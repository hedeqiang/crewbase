// A reply as it streams in, smoothly:
//  - the text is cut into blocks at blank lines outside code fences; every
//    finished block is rendered once (memoised) and never again, so a long
//    answer does not re-parse hundreds of paragraphs at every chunk; only
//    the block still being written renders as it grows
//  - the text shown advances at a steady pace each frame instead of in the
//    lumps the network delivers, faster the further it is behind, so it
//    never lags far; when the reply ends the rest shows at once
//  - a block that has just finished fades in, softly
// The finished message renders with <Markdown> as before.

import { memo, useEffect, useRef, useState } from "react";
import { Markdown } from "./Markdown";

/** Where text can be cut: at a blank line, outside a code fence. */
export function splitBlocks(text: string): string[] {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const out: string[] = [];
  let cur: string[] = [];
  let fence: string | null = null;
  for (const line of lines) {
    const f = line.match(/^\s*(`{3,}|~{3,})/);
    if (f) {
      if (fence === null) fence = f[1];
      else if (f[1][0] === fence[0] && f[1].length >= fence.length && /^\s*(`{3,}|~{3,})\s*$/.test(line)) fence = null;
    }
    if (fence === null && !line.trim() && cur.length && cur.some((l) => l.trim())) {
      out.push(cur.join("\n"));
      cur = [];
      continue;
    }
    cur.push(line);
  }
  if (cur.length) out.push(cur.join("\n"));
  // a list or a table split by a blank line between its items stays one
  // block's worth of look either way: each part is valid Markdown on its own
  return out;
}

const Block = memo(function Block({ text, fresh, last }: { text: string; fresh: boolean; last: boolean }) {
  return (
    <div className={"smblk" + (fresh ? " smnew" : "") + (last ? " smlast" : "")}>
      <Markdown text={text} />
    </div>
  );
});

/** The characters to reveal this frame: a steady pace, faster when far
 *  behind (so a big chunk drains within about a third of a second; catchUp
 *  sets how fast: 3 drains a backlog in ~1/3 s, 1.5 in ~2/3 s). */
function step(behind: number, dtMs: number, catchUp = 3): number {
  const base = 90; // chars per second at least: reading speed and then some
  const perSec = Math.max(base, behind * catchUp);
  return Math.max(1, Math.round((perSec * dtMs) / 1000));
}

/** prefers-reduced-motion, read once per render */
export function reducedMotion(): boolean {
  return typeof window !== "undefined" && !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
}

/** How much of a growing text to show now: advances at a steady pace each
 *  frame (see step) toward the text's length; all of it at once with
 *  reduced motion. Shared by the reply and the thinking window. */
export function usePacedLength(text: string, catchUp = 3, on = true): number {
  const [shown, setShown] = useState(() => text.length);
  const target = useRef(text);
  const last = useRef(0);
  const raf = useRef(0);
  const reduce = reducedMotion() || !on;
  target.current = text;
  useEffect(() => {
    if (reduce) {
      setShown(text.length);
      return;
    }
    const tick = (now: number) => {
      const dt = last.current ? Math.min(64, now - last.current) : 16;
      last.current = now;
      let done = false;
      setShown((n) => {
        const goal = target.current.length;
        if (n >= goal) {
          done = true;
          return goal;
        }
        return Math.min(goal, n + step(goal - n, dt, catchUp));
      });
      if (!done) raf.current = requestAnimationFrame(tick);
      else last.current = 0;
    };
    cancelAnimationFrame(raf.current);
    raf.current = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf.current);
  }, [text, reduce, catchUp]);
  return Math.min(shown, text.length);
}

/** tail: render only the last n blocks (a window that shows the end);
 *  catchUp: how fast a backlog drains (see step); paced false: the text
 *  is already paced by the caller (usePacedLength), show it as it is. */
export function StreamMarkdown({ text, tail, catchUp, paced = true }: { text: string; tail?: number; catchUp?: number; paced?: boolean }) {
  const shown = usePacedLength(text, catchUp, paced);
  // a new reply (the text no longer extends what was shown) starts over
  const visible = text.slice(0, shown);
  const blocks = splitBlocks(visible);
  const seen = useRef(0);
  const freshFrom = seen.current;
  if (blocks.length - 1 > seen.current) seen.current = blocks.length - 1;
  const from = tail ? Math.max(0, blocks.length - tail) : 0;
  return (
    <>
      {blocks.slice(from).map((b, j) => {
        const i = from + j;
        return <Block key={i} text={b} fresh={i >= freshFrom && i < blocks.length - 1} last={i === blocks.length - 1} />;
      })}
    </>
  );
}
