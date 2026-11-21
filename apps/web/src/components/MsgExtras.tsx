import { createContext, useContext, useEffect, useRef, useState } from "react";
import { Copy, CornerUpLeft, Pin, PinOff, Reply, SmilePlus, X } from "lucide-react";
import { API, type AgentView, type Quote } from "../api";
import type { Reactions as ReactionMap } from "../projection";
import { t } from "../i18n";
import { setReply, toast, useStore } from "../store";
import { Popover, useMenu } from "./Popover";

/* Reactions (emoji on a message) and replies (回复, a quoted message),
 * features/reactions and UserMessage.reply. */

export const FeedCtx = createContext<{ session: string; reactions: ReactionMap; pins: number[] } | null>(null);

const QUICK = ["👍", "❤️", "😂", "🎉", "🤔", "👀", "✅", "🙏"];

function whoName(w: string, agents: Record<string, AgentView>) {
  if (!w) return t("rx.you");
  return agents[w]?.name ?? w;
}

/** The emoji under a message; click one to add or take back yours. */
export function Reactions({ target }: { target: string }) {
  const ctx = useContext(FeedCtx);
  const agents = useStore((s) => s.agents);
  const list = ctx?.reactions[target] ?? [];
  // the ones that arrive while it is on screen pop in
  const seen = useRef<Set<string> | null>(null);
  if (seen.current === null) seen.current = new Set(list.map((r) => r.emoji));
  if (!ctx || list.length === 0) return null;
  return (
    <div className="rxs">
      {list.map((r) => {
        const mine = r.who.includes("");
        const fresh = !seen.current!.has(r.emoji);
        const names = r.who.map((w) => whoName(w, agents)).join("、");
        return (
          <button
            key={r.emoji}
            type="button"
            className={"rx" + (mine ? " mine" : "") + (fresh ? " pop" : "")}
            title={t("rx.by", { names, emoji: r.emoji })}
            aria-pressed={mine}
            onAnimationEnd={() => seen.current!.add(r.emoji)}
            onClick={() => void API.react(ctx.session, Number(target), r.emoji, mine).catch((e) => toast(e?.message ?? String(e)))}
          >
            <span className="e">{r.emoji}</span>
            {r.who.length > 1 && <span className="n">{r.who.length}</span>}
          </button>
        );
      })}
    </div>
  );
}

/** 添加回应: a few emoji to choose from. */
export function ReactBtn({ target }: { target: string }) {
  const ctx = useContext(FeedCtx);
  const m = useMenu();
  if (!ctx) return null;
  const mine = new Set((ctx.reactions[target] ?? []).filter((r) => r.who.includes("")).map((r) => r.emoji));
  return (
    <>
      <button type="button" className={"ix mact" + (m.open ? " on" : "")} ref={m.ref} aria-label={t("rx.add")} title={t("rx.add")} onClick={m.toggle}>
        <SmilePlus size={14} />
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} placement="top" className="rxpick">
        {QUICK.map((e) => (
          <button
            key={e}
            type="button"
            className={"rxq" + (mine.has(e) ? " on" : "")}
            aria-label={e}
            onClick={() => {
              m.close();
              void API.react(ctx.session, Number(target), e, mine.has(e)).catch((x) => toast(x?.message ?? String(x)));
            }}
          >
            {e}
          </button>
        ))}
      </Popover>
    </>
  );
}

/** 回复: the next message answers this one. */
export function ReplyBtn({ q }: { q: Quote }) {
  const ctx = useContext(FeedCtx);
  if (!ctx || !q.text) return null;
  return (
    <button
      type="button"
      className="ix mact"
      aria-label={t("rx.reply")}
      title={t("rx.reply")}
      onClick={() => {
        setReply(ctx.session, { ...q, text: excerpt(q.text) });
        document.querySelector<HTMLTextAreaElement>(".composer textarea")?.focus();
      }}
    >
      <Reply size={14} />
    </button>
  );
}

/** a message's text as one plain line: no Markdown (tables, code blocks,
 *  lists, headings, emphasis, links), its first sentence or so */
export function excerpt(text: string, n = 60) {
  const lines = text
    .replace(/```[\s\S]*?(```|$)/g, "\n")
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l && !/^\|/.test(l) && !/^[-=*_]{3,}$/.test(l));
  // a message that opens with a heading is summed up by it
  if (lines.length > 1 && /^#{1,6}\s+/.test(lines[0])) lines.splice(1);
  let one = lines
    .map((l) =>
      l
        .replace(/^#{1,6}\s+/, "")
        .replace(/^>\s?/, "")
        .replace(/^(?:[-*+]|\d+[.)])\s+/, "")
        .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
        .replace(/(\*\*|__|~~|`)/g, "")
        .replace(/(^|\s)[*_]([^*_]+)[*_](?=\s|$)/g, "$1$2"),
    )
    .join(" ")
    .replace(/\s+/g, " ")
    .trim();
  // the first sentence when it is long enough to stand alone
  const m = /^(.{12,}?[。！？!?])\s?/.exec(one);
  if (m && m[1].length <= n) one = m[1];
  return one.length > n ? one.slice(0, n) + "…" : one;
}

/** Scrolls to a message and flashes it. */
export function jumpTo(seq: number) {
  const el = document.querySelector<HTMLElement>(`.feed [data-seq="${seq}"]`);
  if (!el) {
    toast(t("rx.notLoaded"));
    return;
  }
  el.scrollIntoView({ block: "center", behavior: "smooth" });
  el.classList.remove("flash");
  void el.offsetWidth;
  el.classList.add("flash");
  setTimeout(() => el.classList.remove("flash"), 1600);
}

/** The quote above a reply; click it to see the message. */
export function QuoteLine({ q, mine }: { q: Quote; mine?: boolean }) {
  return (
    <button type="button" className={"qline" + (mine ? " mine" : "")} onClick={() => jumpTo(q.seq)} title={t("rx.jump")}>
      <CornerUpLeft size={12} aria-hidden />
      <b>{q.who}</b>
      <span>{q.text}</span>
    </button>
  );
}

/** Above the composer while the next message is a reply. */
export function ReplyChip({ session }: { session: string }) {
  const q = useStore((s) => s.replyTo[session]);
  const [shown, setShown] = useState(q);
  useEffect(() => {
    if (q) setShown(q);
  }, [q]);
  if (!q || !shown) return null;
  return (
    <div className="replychip">
      <CornerUpLeft size={13} aria-hidden />
      <button type="button" className="rcq" onClick={() => jumpTo(q.seq)} title={t("rx.jump")}>
        <span className="muted">{t("rx.replying", { who: q.who })}</span>
        <span className="rct">{q.text}</span>
      </button>
      <button type="button" className="ix" aria-label={t("rx.cancel")} title={t("rx.cancel")} onClick={() => setReply(session, null)}>
        <X size={13} />
      </button>
    </div>
  );
}

/** 置顶 / 取消置顶 */
export function PinBtn({ target }: { target: number }) {
  const ctx = useContext(FeedCtx);
  if (!ctx) return null;
  const on = ctx.pins.includes(target);
  return (
    <button
      type="button"
      className={"ix mact" + (on ? " on" : "")}
      aria-label={t(on ? "pin.off" : "pin.on")}
      title={t(on ? "pin.off" : "pin.on")}
      aria-pressed={on}
      onClick={() => void API.pin(ctx.session, target, on).then(() => toast(t(on ? "pin.offDone" : "pin.onDone")), (e) => toast(e?.message ?? String(e)))}
    >
      {on ? <PinOff size={14} /> : <Pin size={14} />}
    </button>
  );
}

/** At the top of the conversation: the pinned messages, the newest shown,
 *  the others one click away. */
export function PinnedBar({ items }: { items: { seq: number; who: string; text: string }[] }) {
  const ctx = useContext(FeedCtx);
  const [i, setI] = useState(0);
  if (!ctx || items.length === 0) return null;
  const k = Math.min(i, items.length - 1);
  const p = items[k];
  return (
    <div className="pinbar" key={items.length}>
      <Pin size={13} className="pinic" aria-hidden />
      <button type="button" className="pinmain" onClick={() => jumpTo(p.seq)} title={t("rx.jump")}>
        <b>{p.who}</b>
        <span>{p.text}</span>
      </button>
      {items.length > 1 && (
        <button type="button" className="pinn" onClick={() => setI((k + 1) % items.length)} title={t("pin.next")}>
          {k + 1}/{items.length}
        </button>
      )}
      <button type="button" className="ix" aria-label={t("pin.off")} title={t("pin.off")} onClick={() => void API.pin(ctx.session, p.seq, true)}>
        <X size={13} />
      </button>
    </div>
  );
}

/** Text selected in a reply: quote it in a reply, or copy it. */
export function SelectionActions() {
  const ctx = useContext(FeedCtx);
  const [at, setAt] = useState<{ x: number; y: number; text: string; seq: number; who: string } | null>(null);
  useEffect(() => {
    const feed = document.querySelector(".feed");
    if (!feed) return;
    const check = () => {
      const sel = window.getSelection();
      const text = sel?.toString().trim() ?? "";
      if (!sel || sel.rangeCount === 0 || !text) return setAt(null);
      const range = sel.getRangeAt(0);
      const host = (range.commonAncestorContainer instanceof Element ? range.commonAncestorContainer : range.commonAncestorContainer.parentElement)?.closest<HTMLElement>("[data-seq]");
      if (!host || !feed.contains(host)) return setAt(null);
      const r = range.getBoundingClientRect();
      const who = host.querySelector(".mh b")?.textContent || host.closest(".turn")?.querySelector(".mh b")?.textContent || (host.classList.contains("m-user") ? t("rx.you") : "");
      setAt({ x: r.left + r.width / 2, y: r.top, text, seq: Number(host.dataset.seq), who });
    };
    const up = () => setTimeout(check, 0);
    const change = () => {
      if (!window.getSelection()?.toString().trim()) setAt(null);
    };
    const scroll = () => setAt(null);
    feed.addEventListener("mouseup", up);
    document.addEventListener("selectionchange", change);
    feed.addEventListener("scroll", scroll, { passive: true });
    return () => {
      feed.removeEventListener("mouseup", up);
      document.removeEventListener("selectionchange", change);
      feed.removeEventListener("scroll", scroll);
    };
  }, [ctx?.session]);
  if (!ctx || !at) return null;
  const quote = () => {
    setReply(ctx.session, { seq: at.seq, who: at.who, text: excerpt(at.text, 200) });
    window.getSelection()?.removeAllRanges();
    setAt(null);
    document.querySelector<HTMLTextAreaElement>(".composer textarea")?.focus();
  };
  return (
    <div className="selacts ui-pop" style={{ left: at.x, top: at.y }} onMouseDown={(e) => e.preventDefault()}>
      <button type="button" onClick={quote}>
        <Reply size={13} />
        {t("sel.quote")}
      </button>
      <button
        type="button"
        onClick={() => {
          void navigator.clipboard?.writeText(at.text).then(() => toast(t("msg.copied")));
          setAt(null);
        }}
      >
        <Copy size={13} />
        {t("msg.copy")}
      </button>
    </div>
  );
}
