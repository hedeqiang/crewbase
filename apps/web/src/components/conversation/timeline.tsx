// How a turn's work reads: what it thought, what it said on the way and the
// tools it called, in the order they happened, as one calm timeline.
//  - a run of thinking, short narration and tool calls is one segment; a
//    finished turn shows each segment as one line (跑了 2 条命令 · 看了 1 个
//    文件 · 思考 3 次) that opens in place; a running turn shows it open, its
//    latest items (older ones fold into 前面还有 N 项, so it does not grow
//    without end)
//  - the structure is the same while it runs and after, so nothing jumps
//    when the turn ends: only what the person opens or closes changes
//  - thinking streams in a short window that rolls upward as lines come
//    (StreamMarkdown's pace); when the reply starts it settles into a
//    one-line 思考了 N 秒 row that stays, and opens in place

import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Brain, Check, ChevronRight, CircleMinus, CircleX, Hand, Search as SearchIcon } from "lucide-react";
import { t } from "../../i18n";
import { type AgentItem, type Step, type TurnPart } from "../../projection";
import { Markdown } from "../Markdown";
import { StreamMarkdown, reducedMotion, usePacedLength } from "../StreamMarkdown";
import { durLabel, outcome, StepRow, useTick, workSummary } from "./steps";

export type TlItem =
  | { k: "think"; id: string; text: string; agent: string; ms?: number }
  | { k: "narr"; id: string; it: AgentItem }
  | { k: "step"; id: string; s: Step };

export type Piece = TurnPart | { k: "seg"; id: string; items: TlItem[] };

/** Narration shorter than this is part of the work; longer, it is said
 *  to the person and stays in view. */
const SHORT_NARR = 240;

/** A turn's parts as rendered: runs of thinking, short narration and steps
 *  become segments; answers, long narration and cards stay as they are. */
export function timeline(parts: TurnPart[], open: boolean): Piece[] {
  const out: Piece[] = [];
  const g: { seg: TlItem[] | null } = { seg: null };
  const add = (x: TlItem) => {
    if (!g.seg) {
      g.seg = [];
      out.push({ k: "seg", id: x.id, items: g.seg });
    }
    g.seg.push(x);
  };
  const keep = (p: TurnPart) => {
    g.seg = null;
    out.push(p);
  };
  let afterMedia = false;
  for (const p of parts) {
    if (p.k === "steps") {
      for (const s of p.steps) add({ k: "step", id: s.call.id, s });
      continue;
    }
    if (p.k !== "msg") {
      if (p.k === "media") afterMedia = true;
      keep(p);
      continue;
    }
    const it = p.it;
    if (it.thinking) add({ k: "think", id: it.id + ":t", text: it.thinking, agent: it.agent, ms: it.tookMs });
    if (!p.narration) {
      keep(p);
      continue;
    }
    if (!it.text && !it.searches.length) continue;
    // after the pictures they made, the words fold on their own (as before)
    if (afterMedia && !open && it.text) keep(p);
    else if ((it.text ?? "").length < SHORT_NARR) add({ k: "narr", id: it.id, it });
    else keep(p);
  }
  return out;
}

// ------------------------------------------------------------- thinking

/** How long thoughts took as watched live (the log only knows it for a
 *  model call that wrote nothing else): by agent and how the thought begins. */
const watched = new Map<string, number>();
const thinkKey = (agent: string, text: string) => agent + "|" + text.trim().slice(0, 80);
export const thoughtMs = (agent: string, text: string, ms?: number) => ms ?? watched.get(thinkKey(agent, text));

function thoughtExcerpt(text: string): string {
  const line = text
    .split("\n")
    .map((l) => l.replace(/^[#>*\-\d.\s`]+/, "").replace(/[*_`]/g, "").trim())
    .find(Boolean);
  return line ?? "";
}

/** A thought as one line (思考了 6 秒 · how it began); opens in place to the
 *  whole thought as soft Markdown. */
export function ThoughtRow({ text, ms }: { text: string; ms?: number }) {
  const [open, setOpen] = useState(false);
  // a known time says so, however short (under a second reads as 1 秒)
  const dur = ms !== undefined && ms > 0 ? durLabel(Math.max(1000, ms)) : "";
  return (
    <div className={"tlitem think" + (open ? " on" : "")}>
      <button className="tlrow" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className="tlic">
          <Brain size={13} />
        </span>
        <span className="tll">{dur ? t("chat.thoughtFor", { dur }) : t("chat.thought")}</span>
        {!open && <span className="tlx">{thoughtExcerpt(text)}</span>}
        <ChevronRight size={13} className="ch" />
      </button>
      {open && (
        <div className="thinkmd mt">
          <Markdown text={text} />
        </div>
      )}
    </div>
  );
}

/** The thought being written: 思考中 with its clock, and the text rolling
 *  up in a short window; once the reply starts (writing), the one-line row.
 *  With no text yet it is the 思考中 line alone (label: what it is doing). */
export function LiveThought({ thinking, writing, agent, label }: { thinking: string; writing: boolean; agent: string; label?: string }) {
  const start = useRef(Date.now());
  const end = useRef<number | null>(null);
  const last = useRef(thinking);
  last.current = thinking;
  if (writing && end.current === null && thinking) {
    end.current = Date.now();
    watched.set(thinkKey(agent, thinking), end.current - start.current);
  }
  useEffect(
    () => () => {
      // a thought that ended in a tool call: it stopped when it went away
      if (end.current === null && last.current) watched.set(thinkKey(agent, last.current), Date.now() - start.current);
    },
    [agent],
  );
  // the window rolls at its own pace; once the reply starts it finishes
  // rolling what came (a moment at most), then settles into its row
  const shown = usePacedLength(thinking, 1.5);
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (!writing) return;
    const id = setTimeout(() => setLate(true), SETTLE_MS);
    return () => clearTimeout(id);
  }, [writing]);
  const settled = writing && (shown >= thinking.length || late);
  useTick(!writing);
  if (settled) return thinking ? <ThoughtRow text={thinking} ms={end.current! - start.current} /> : null;
  const secs = durLabel((end.current ?? Date.now()) - start.current);
  return (
    <div className="tlitem think live">
      <div className="tlrow" role="status">
        <span className="tlic">
          <Brain size={13} className="thinkpulse" />
        </span>
        <span className="tll">{label || t("chat.thinkingNow")}</span>
        <span className="tlmeta">{secs}</span>
      </div>
      {thinking && <RollWindow text={thinking.slice(0, shown)} />}
    </div>
  );
}

/** At most this long rolling on once the reply has started. */
const SETTLE_MS = 1200;

/** A few lines tall, the newest at the bottom: as a line is added the text
 *  slides up by its height (and the window grows by it until it is full),
 *  the oldest lines fading out at the top. */
function RollWindow({ text }: { text: string }) {
  const box = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const b = box.current;
    const el = inner.current;
    if (!b || !el) return;
    const reduce = reducedMotion();
    const max = () => parseFloat(getComputedStyle(b).maxHeight) || 96;
    let prev = el.offsetHeight;
    b.style.height = Math.min(prev, max()) + "px";
    const ro = new ResizeObserver(() => {
      const h = el.offsetHeight;
      const d = h - prev;
      prev = h;
      const m = max();
      b.classList.toggle("full", h > m + 1);
      if (reduce || d <= 0) {
        b.style.height = Math.min(h, m) + "px";
        return;
      }
      // where the text is now (mid-slide, perhaps), pushed back down by
      // what was added, then let go: it slides up to rest
      const cur = new DOMMatrixReadOnly(getComputedStyle(el).transform).m42 || 0;
      el.style.transition = "none";
      el.style.transform = `translateY(${cur + d}px)`;
      void el.offsetHeight;
      el.style.transition = "";
      el.style.transform = "translateY(0)";
      b.style.height = Math.min(h, m) + "px";
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <div className="thinkroll" ref={box} aria-hidden>
      <div className="thinkroll-in mt" ref={inner}>
        <StreamMarkdown text={text} tail={6} paced={false} />
      </div>
    </div>
  );
}

// ------------------------------------------------------------- segments

/** Items shown at the end of a running segment; older ones fold. */
const TAIL = 6;

function Item({ x, live, agent }: { x: TlItem; live: boolean; agent?: string }) {
  if (x.k === "step") return <StepRow s={x.s} agent={agent} live={live} />;
  if (x.k === "think") return <ThoughtRow text={x.text} ms={thoughtMs(x.agent, x.text, x.ms)} />;
  return (
    <div className={"tlitem narr" + (live ? " fresh" : "")}>
      <span className="tlic">
        <span className="tldot" />
      </span>
      <div className="tlbody">
        {x.it.searches.map((q, i) => (
          <div className="srvtool" key={i}>
            <SearchIcon size={13} />
            <span>{t("chat.searched", { q })}</span>
          </div>
        ))}
        {x.it.text && (
          <div className="mt">
            <Markdown text={x.it.text} />
          </div>
        )}
      </div>
    </div>
  );
}

/** What a segment did, in words: the work first, then the thinking. */
function segSummary(items: TlItem[]): string {
  const steps = items.flatMap((x) => (x.k === "step" ? [x.s] : []));
  const thoughts = items.filter((x): x is Extract<TlItem, { k: "think" }> => x.k === "think");
  const parts: string[] = [];
  const work = workSummary(steps);
  if (work) parts.push(work);
  if (thoughts.length) {
    const ms = thoughts.map((x) => thoughtMs(x.agent, x.text, x.ms));
    const sum = ms.every((m) => m !== undefined) ? ms.reduce((a, b) => a! + b!, 0)! : 0;
    const known = sum > 0 ? durLabel(Math.max(1000, sum)) : "";
    parts.push(thoughts.length > 1 ? t("chat.thoughtN", { n: thoughts.length }) + (known ? " · " + known : "") : known ? t("chat.thoughtFor", { dur: known }) : t("chat.thought"));
  }
  if (!parts.length) parts.push(t("tl.notes", { n: items.length }));
  return parts.join(" · ");
}

/** One segment: a header line that opens and closes it, then its items in
 *  order. live: the turn is running; active: and this is where it is now
 *  (tail: what is happening at the end, the live thought); after: the turn
 *  went on past it (a failure it recovered from is a pill, not the mark). */
export function Segment({ items, live, active, agent, tail, after }: { items: TlItem[]; live: boolean; active?: boolean; agent?: string; tail?: ReactNode; after?: boolean }) {
  const [mode, setMode] = useState<"tail" | "open" | "closed">(live ? "tail" : "closed");
  // one item needs no header line, unless more is coming (a header that
  // appeared later would push what is on screen down); once there, it stays
  const head = useRef(false);
  head.current ||= items.length > 1 || (live && !!tail);
  if (!head.current)
    return (
      <div className={"tl" + (tail ? "" : " solo")}>
        <Item x={items[0]} live={live} agent={agent} />
        {tail}
      </div>
    );
  const steps = items.flatMap((x) => (x.k === "step" ? [x.s] : []));
  const running = live && steps.some((s) => !s.result && !s.awaiting);
  const awaiting = steps.some((s) => s.awaiting);
  const errors = steps.filter((s) => s.result && outcome(s) === "error").length;
  const declined = steps.filter((s) => s.result && outcome(s) === "declined").length;
  const cut = !live && steps.some((s) => !s.result && !s.awaiting);
  const shown = mode === "closed" ? [] : mode === "tail" && items.length > TAIL + 1 ? items.slice(-TAIL) : items;
  const hidden = mode === "closed" ? 0 : items.length - shown.length;
  return (
    <div className={"tl tlseg" + (mode === "closed" ? " closed" : "")}>
      <button className="tlrow seghead" type="button" aria-expanded={mode !== "closed"} onClick={() => setMode(mode === "closed" ? "open" : "closed")}>
        <span className="tlic">
          {awaiting ? (
            <Hand size={13} className="waitc" />
          ) : running || active ? (
            <span className="spin sm" />
          ) : errors && !live && !after ? (
            <CircleX size={13} className="errc" />
          ) : cut ? (
            <CircleMinus size={13} className="muted" />
          ) : (
            <Check size={13} className="okc" />
          )}
        </span>
        <span className="tll segsum">{segSummary(items)}</span>
        {awaiting && <span className="pill sm amber">{t(steps.some((s) => s.awaiting && s.asking) ? "el.waiting" : "appr.waiting")}</span>}
        {declined > 0 && <span className="pill sm">{t("chat.toolDeclined")}</span>}
        {errors > 0 && (
          <span className="pill sm red">
            {t("chat.toolError")} {errors}
          </span>
        )}
        <ChevronRight size={13} className="ch" />
      </button>
      {hidden > 0 && (
        <button className="tlrow tlmore" type="button" onClick={() => setMode("open")}>
          <span className="tlic">
            <span className="tldot" />
          </span>
          <span className="tll">{t("tl.earlier", { n: hidden })}</span>
        </button>
      )}
      {shown.map((x) => (
        <Item key={x.id} x={x} live={live} agent={agent} />
      ))}
      {tail}
    </div>
  );
}

/** Steps on their own (no turn around them yet): one segment. */
export function Steps({ steps, agent }: { steps: Step[]; agent?: string }) {
  const live = steps.some((s) => !s.result);
  return <Segment items={steps.map((s) => ({ k: "step", id: s.call.id, s }))} live={live} agent={agent} />;
}
