import { createContext, useContext, useEffect, useRef, useState } from "react";
import { CornerUpLeft, Reply, SmilePlus, X } from "lucide-react";
import { API, type AgentView, type Quote } from "../api";
import type { Reactions as ReactionMap } from "../projection";
import { t } from "../i18n";
import { setReply, toast, useStore } from "../store";
import { Popover, useMenu } from "./Popover";

/* Reactions (emoji on a message) and replies (回复, a quoted message),
 * features/reactions and UserMessage.reply. */

export const FeedCtx = createContext<{ session: string; reactions: ReactionMap } | null>(null);

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

/** a message's text as a one-line excerpt */
export function excerpt(text: string, n = 140) {
  const one = text.replace(/```[\s\S]*?```/g, " … ").replace(/[#>*_`]+/g, "").replace(/\s+/g, " ").trim();
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
