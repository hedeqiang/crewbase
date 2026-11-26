import { UndoTurnBtn } from "../TurnFiles";
import { FeedCtx, PinBtn, QuoteLine, ReactBtn, Reactions, ReplyBtn } from "../MsgExtras";
import { PageCard } from "../PageCard";
import { useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useCopy } from "../../clipboard";
import { Check, Search as SearchIcon, Info, Volume2, Square, Copy, Pencil, RefreshCw } from "lucide-react";
import { MAC } from "../../shortcuts";
import { type AgentView } from "../../api";
import { UsedCmds } from "../SlashMenu";
import { t } from "../../i18n";
import { type AgentItem, type FeedItem, type TurnPart, type TurnStats } from "../../projection";
import { toast, useStore } from "../../store";
import { speak, speaking, speakSupported } from "../../voice";
import { MsgAttachments } from "../Attachments";
import { MediaCard } from "../Media";
import { LocalPreviewCards, PreviewLine } from "../Preview";
import { ArtifactCard } from "../ArtifactCard";
import { detailLabel, hhmm, modelLabel, shortModelEffort, replyUsage, tokfmt, costfmt, numLocale } from "../../format";
import { Avatar } from "../Avatar";
import { Markdown } from "../Markdown";
import { StreamMarkdown } from "../StreamMarkdown";
import { LinkCards } from "../LinkCards";
import { DocCards } from "../DocCards";
import { MsgCtx, SessionCtx } from "./context";
import { siteOf, speaker } from "./util";
import { lastMsg } from "./feedUtil";
import { FoldedWords, withoutMediaSteps } from "./steps";
import { LiveThought, Segment, ThoughtRow, thoughtMs, timeline } from "./timeline";
import { splitStarters, Starters } from "./other";
import { AutoDraftBtn, AutoDraftSlot } from "./autoDraft";

function CopyBtn({ text }: { text: string }) {
  const { copied, copy } = useCopy();
  const label = copied ? t("msg.copied") : t("msg.copy");
  return (
    <button type="button" className={"ix mact" + (copied ? " on" : "")} aria-label={label} title={label} onClick={() => void copy(text).then((ok) => ok || toast(t("msg.copyFailed")))}>
      {copied ? <Check size={14} /> : <Copy size={14} />}
    </button>
  );
}

/** Model · speed on the left; tokens and cost of the whole turn on the right. */
function ReplyStats({ stats }: { stats: TurnStats }) {
  const models = useStore((s) => s.models);
  const u = replyUsage(models, stats);
  const total = u.input + u.output;
  const name = u.model ? shortModelEffort(models, u.model) : null;
  const tokTitle = t("msg.tokensTitle", {
    calls: u.calls,
    input: numLocale(u.input),
    cache: u.cacheRead ? t("msg.cacheRead", { n: numLocale(u.cacheRead) }) : "",
    output: numLocale(u.output),
  });
  return (
    <div className="replystats">
      <span className="rs-l">
        {name && u.model && (
          <span className="rs-model" title={modelLabel(models, u.model)}>
            {name.name}
            {name.effort && <span className="effs">{name.effort}</span>}
          </span>
        )}
        {u.speed !== undefined && (
          <span title={t("msg.speedTitle")}>
            {name ? " · " : ""}
            {t("msg.speed", { n: u.speed >= 100 ? Math.round(u.speed) : u.speed.toFixed(1) })}
          </span>
        )}
      </span>
      {total > 0 && (
        <span className="rs-r">
          <span className="rs-tok" title={tokTitle} aria-label={tokTitle}>
            {tokfmt(total)} tokens
          </span>
          {u.cost !== undefined && (
            <span className="rs-cost" title={t("msg.costTitle")}>
              {costfmt(u.cost)}
            </span>
          )}
          {u.local && (
            <span className="rs-cost free" title={t("msg.localTitle")}>
              {t("msg.local")}
            </span>
          )}
        </span>
      )}
    </div>
  );
}

/** A user message: copy, and edit and resend in a one-to-one conversation. */
/** Text with "@Name" / "@id" of known agents shown as mention chips. */
function WithMentions({ text }: { text: string }) {
  const agents = useStore((s) => s.agents);
  const parts = useMemo(() => {
    if (!text.includes("@")) return null;
    const handles = new Set<string>();
    for (const a of Object.values(agents)) {
      handles.add(a.name);
      handles.add(a.id);
    }
    const list = [...handles].filter(Boolean).sort((a, b) => b.length - a.length);
    if (!list.length) return null;
    const esc = list.map((h) => h.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
    const re = new RegExp("(^|[\\s(（，,。])@(" + esc.join("|") + ")(?=$|[\\s,，。.!！?？:：)）])", "g");
    const out: (string | { h: string })[] = [];
    let last = 0;
    for (const m of text.matchAll(re)) {
      const at = m.index! + m[1].length;
      if (at > last) out.push(text.slice(last, at));
      out.push({ h: m[2] });
      last = at + 1 + m[2].length;
    }
    if (!out.length) return null;
    if (last < text.length) out.push(text.slice(last));
    return out;
  }, [text, agents]);
  if (!parts) return <>{text}</>;
  return (
    <>
      {parts.map((x, i) => (typeof x === "string" ? x : <span key={i} className="mchip">@{x.h}</span>))}
    </>
  );
}

/** Characters of a user message shown before it folds. */
const LONG_MSG = 400;

export function UserMsg({ it }: { it: Extract<FeedItem, { k: "user" }> }) {
  const acts = useContext(MsgCtx);
  const long = (it.text?.length ?? 0) > LONG_MSG * 1.5;
  const [expanded, setExpanded] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(it.text);
  const [busy, setBusy] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const editable = !!acts && !it.superseded && it.seq > acts.compactedUpto && (!it.to || it.to === acts.agent.id) && !!it.text;
  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 320) + "px";
  }, [draft, editing]);
  useEffect(() => {
    if (editing) {
      const el = ta.current;
      el?.focus();
      el?.setSelectionRange(el.value.length, el.value.length);
    }
  }, [editing]);
  const submit = async () => {
    if (!acts || !draft.trim() || busy || !acts.idle) return;
    setBusy(true);
    const ok = await acts.resend(it.seq, draft);
    setBusy(false);
    if (ok) setEditing(false);
  };
  if (editing && acts)
    return (
      <div className="m-user">
        <div className="bubble editing">
          <textarea
            ref={ta}
            value={draft}
            aria-label={t("msg.edit")}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                setEditing(false);
              } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
                e.preventDefault();
                void submit();
              }
            }}
          />
          <div className="editfoot">
            <span className="small">{t("msg.editHint")}</span>
            <span className="editbtns">
              <button type="button" className="btn ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </button>
              <button
                type="button"
                className="btn pri"
                disabled={!draft.trim() || busy || !acts.idle}
                title={acts.idle ? t("msg.editKeys", { key: MAC ? "⌘↩" : "Ctrl+↩" }) : t("msg.busy", { name: acts.agent.name })}
                onClick={() => void submit()}
              >
                {t("msg.send")}
              </button>
            </span>
          </div>
        </div>
      </div>
    );
  return (
    <div className="m-user" data-seq={it.seq}>
      {it.reply && <QuoteLine q={it.reply} mine />}
      {it.attachments?.length ? <MsgAttachments agent={it.to} items={it.attachments} /> : null}
      {(it.text || it.use?.length || !it.attachments?.length) && (
        <div className={"bubble" + (long && !expanded ? " clipped" : "")}>
          {it.from && <span className="from">{it.from}</span>}
          <UsedCmds use={it.use} />
          <WithMentions text={long && !expanded ? it.text.slice(0, LONG_MSG) + "…" : it.text} />
          {long && (
            <button type="button" className="bubmore" onClick={() => setExpanded(!expanded)}>
              {expanded ? t("msg.collapse") : t("msg.expand", { n: it.text.length.toLocaleString() })}
            </button>
          )}
        </div>
      )}
      {it.text && <LinkCards text={it.text} mine />}
      <Reactions target={String(it.seq)} />
      {it.text && (
        <div className="msgacts">
          <ReactBtn target={String(it.seq)} />
          <ReplyBtn q={{ seq: it.seq, who: it.from || t("rx.you"), text: it.text }} />
          <PinBtn target={it.seq} />
          {editable && (
            <button
              type="button"
              className="ix mact"
              disabled={!acts!.idle}
              aria-label={t("msg.edit")}
              title={acts!.idle ? t("msg.editTitle") : t("msg.busy", { name: acts!.agent.name })}
              onClick={() => {
                setDraft(it.text);
                setEditing(true);
              }}
            >
              <Pencil size={14} />
            </button>
          )}
          <CopyBtn text={it.text} />
        </div>
      )}
    </div>
  );
}

export function AgentMsg({ it, agent, showModel, bare, opened, extra, fold }: { it: AgentItem; agent?: AgentView; showModel: boolean; bare?: boolean; opened?: string[]; extra?: ReactNode; fold?: boolean }) {
  const models = useStore((s) => s.models);
  const [reading, setReading] = useState(false);
  const acts = useContext(MsgCtx);
  const feedSession = useContext(FeedCtx)?.session;
  const [regen, setRegen] = useState(false);
  const sm = it.model ? shortModelEffort(models, it.model) : null;
  // regenerate: the last finished turn of a one-to-one conversation
  const canRegen = !!acts && !it.superseded && acts.lastTurn?.item === it.id && acts.lastTurn.from > acts.compactedUpto;
  const hasActs = !!it.text || canRegen || !!(it.stats && it.turnInput);
  return (
    <Wrap bare={bare} final={!!it.stats} agent={agent} seq={it.id}>
        {!bare && <div className="mh">
          <b>{agent ? speaker(agent) : it.agent}</b>
          <span className="t">{hhmm(it.time)}</span>
          {/* the turn's last reply names its model in the footer */}
          {sm && it.model && !it.stats && (
            <span className={"via" + (showModel ? "" : " hov")} title={modelLabel(models, it.model)}>
              {sm.name}
              {sm.effort && <span className="effs">{sm.effort}</span>}
            </span>
          )}
        </div>}
        {!bare && it.thinking && (
          <div className="tl solo">
            <ThoughtRow text={it.thinking} ms={thoughtMs(it.agent, it.thinking, it.tookMs)} />
          </div>
        )}
        {it.searches.map((q, i) => (
          <div className="srvtool" key={i}>
            <SearchIcon size={13} />
            <span>{t("chat.searched", { q })}</span>
          </div>
        ))}
        {it.text &&
          (fold ? (
            <FoldedWords text={it.text}>
              <div className="mt">
                <Markdown text={it.text} />
              </div>
            </FoldedWords>
          ) : (
            <div className="mt">
              <Markdown text={it.text} />
            </div>
          ))}
        {it.text && <LocalPreviewCards text={it.text} opened={opened} />}
        {it.text && <DocCards text={it.text} />}
        {it.refusal && (
          <div className="refusal">
            <Info size={14} />
            <span>{t("chat.refusal", { name: agent?.name ?? it.agent })}</span>
          </div>
        )}
        {it.citations.length > 0 && (
          <div className="cites">
            <span className="small">{t("chat.sources")}</span>
            {it.citations.map((c, i) => (
              <a key={c.url} className="cite" href={c.url} target="_blank" rel="noreferrer noopener" title={c.url}>
                <span className="n">{i + 1}</span>
                <span className="site">{siteOf(c.url)}</span>
                <span className="ct">{c.title}</span>
              </a>
            ))}
          </div>
        )}
        {/* the answer, once the turn is over (never narration or a stream) */}
        {it.stats && it.text && <LinkCards text={it.text} exclude={it.asked} />}
        {extra}
        {it.stats && <ReplyStats stats={it.stats} />}
        <Reactions target={it.id} />
        {hasActs && (
          <div className="msgacts">
            {it.text && <CopyBtn text={it.text} />}
            {it.text && <ReactBtn target={it.id} />}
            {it.text && <ReplyBtn q={{ seq: Number(it.id), who: agent?.name ?? it.agent, text: it.text }} />}
            {it.text && <PinBtn target={Number(it.id)} />}
            {it.stats && it.turnInput && it.turnEnded && <UndoTurnBtn agent={it.agent} turn={it.turnInput} ended={it.turnEnded} session={feedSession} seq={Number(it.id)} />}
            {it.stats && it.turnEnded && feedSession && !it.superseded && <AutoDraftBtn session={feedSession} item={it.id} agent={it.agent} />}
            {canRegen && (
              <button
                type="button"
                className="ix mact"
                disabled={!acts!.idle || regen}
                aria-label={t("msg.regenerate")}
                title={acts!.idle ? t("msg.regenerateTitle") : t("msg.busy", { name: acts!.agent.name })}
                onClick={async () => {
                  setRegen(true);
                  await acts!.resend(acts!.lastTurn!.from);
                  setRegen(false);
                }}
              >
                <RefreshCw size={14} />
              </button>
            )}
            {it.text && speakSupported() && (
              <button
                type="button"
                className={"ix mact" + (reading ? " on" : "")}
                aria-label={reading ? t("voice.stop") : t("voice.read")}
                title={reading ? t("voice.stop") : t("voice.read")}
                onClick={() => {
                  setReading(!speaking(it.id));
                  speak(it.id, it.text, () => setReading(false));
                }}
              >
                {reading ? <Square size={12} /> : <Volume2 size={14} />}
              </button>
            )}
          </div>
        )}
        {it.stats && feedSession && <AutoDraftSlot session={feedSession} item={it.id} />}
    </Wrap>
  );
}

/** A message's frame: avatar and column, or nothing inside a turn. */
function Wrap({ bare, final, agent, seq, children }: { bare?: boolean; final?: boolean; agent?: AgentView; seq?: string; children: ReactNode }) {
  if (bare) return <div className="tans" data-seq={seq}>{children}</div>;
  return (
    <div className={"m-agent" + (final ? " final" : "")} data-seq={seq}>
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>{children}</div>
    </div>
  );
}

/** One agent's turn: one header, then what it did in order: its work as
 *  timeline segments (thinking, short narration and tool calls; see
 *  timeline.tsx), longer words and cards in view, and its answer; while it
 *  runs, what it thinks rolls at the end of the timeline and the text being
 *  written grows below it. */
export function TurnMsg({ it, agent }: { it: Extract<FeedItem, { k: "turn" }>; agent?: AgentView; showModel?: boolean }) {
  const models = useStore((s) => s.models);
  const sid = useContext(SessionCtx);
  const live = useStore((s) => (it.open ? s.streams[sid + "|" + it.agent] : undefined));
  const waiting = useStore((s) => s.approvals.some((a) => a.agent === it.agent && a.session === sid));
  // between steps, with nothing being written yet: say it is working
  const lastPart = it.parts[it.parts.length - 1];
  const stepRunning = lastPart?.k === "steps" && lastPart.steps.some((x) => !x.result);
  const working = it.open && agent?.status === "running" && !waiting && !stepRunning && !live?.text;
  const msgs = it.parts.filter((p): p is Extract<TurnPart, { k: "msg" }> => p.k === "msg");
  // previews this turn already opened: no 在右侧预览 card again for them
  const opened = it.parts.flatMap((p) => (p.k === "preview" ? [p.p.url] : []));
  const last = lastMsg(it);
  const answer = msgs.find((p) => !p.narration && p.it.stats)?.it;
  const sm = last?.model ? shortModelEffort(models, last.model) : null;
  const pieces = timeline(withoutMediaSteps(it.parts), !!it.open);
  const lastPiece = pieces[pieces.length - 1];
  // what it is doing now, at the end of the timeline: thinking (rolling),
  // or 思考中 between steps before anything streams
  const thinkingNow = live?.thinking ?? "";
  const liveNode =
    it.open && (thinkingNow || working) ? (
      <LiveThought key="live" thinking={thinkingNow} writing={!!live?.text} agent={it.agent} label={working && agent ? detailLabel(agent.detail) : ""} />
    ) : null;
  return (
    <div className={"m-agent turn" + (answer ? " final" : "")}>
      <Avatar agent={agent} size={28} />
      <div style={{ minWidth: 0 }}>
        <div className="mh">
          <b>{agent ? speaker(agent) : it.agent}</b>
          <span className="t">{hhmm(it.time)}</span>
          {/* while it works, which model; a finished turn names it in its footer */}
          {sm && last?.model && !answer && (
            <span className="via" title={modelLabel(models, last.model)}>
              {sm.name}
              {sm.effort && <span className="effs">{sm.effort}</span>}
            </span>
          )}
        </div>
        {pieces.map((p, i, all) =>
          p.k === "seg" ? (
            <Segment key={p.id} items={p.items} live={!!it.open} active={!!it.open && i === all.length - 1 && !live?.text} agent={it.agent} tail={i === all.length - 1 ? liveNode : null} after={i < all.length - 1} />
          ) : // after the pictures they made, the words are a click away
          p.k === "msg" && p.narration && p.it.text && !it.open && all.slice(0, i).some((x) => x.k === "media") ? (
            <FoldedWords key={p.it.id} text={p.it.text}>
              <div className="narr">
                <div className="mt">
                  <Markdown text={p.it.text} />
                </div>
              </div>
            </FoldedWords>
          ) : p.k === "steps" ? null : p.k === "media" ? (
            <MediaCard key={p.id} it={p.m} inTurn />
          ) : p.k === "preview" ? (
            <PreviewLine key={p.id} p={p.p} session={sid} />
          ) : p.k === "artifact" ? (
            <ArtifactCard key={p.id} a={p.a} session={sid} />
          ) : p.k === "page" ? (
            <PageCard key={p.id} p={p.p} session={sid} />
          ) : p.narration ? (
            p.it.text || p.it.searches.length ? (
              <div className={"narr" + (it.open ? " fresh" : "")} key={p.it.id}>
                {p.it.searches.map((q, i) => (
                  <div className="srvtool" key={i}>
                    <SearchIcon size={13} />
                    <span>{t("chat.searched", { q })}</span>
                  </div>
                ))}
                {p.it.text && (
                  <div className="mt">
                    <Markdown text={p.it.text} />
                  </div>
                )}
              </div>
            ) : null
          ) : it.greeting ? (
            <AgentMsg
              key={p.it.id}
              it={{ ...p.it, text: splitStarters(p.it.text).text }}
              agent={agent}
              showModel={false}
              bare
              opened={opened}
              extra={it.starters && !it.open && agent ? <Starters agent={agent} items={splitStarters(p.it.text).starters} /> : null}
            />
          ) : (
            <AgentMsg key={p.it.id} it={p.it} agent={agent} showModel={false} bare opened={opened} fold={!it.open && all.slice(0, i).some((x) => x.k === "media")} />
          ),
        )}
        {liveNode && lastPiece?.k !== "seg" && <div className="tl">{liveNode}</div>}
        {live?.text && (
          <div className="tans">
            <div className="mt streaming">
              <StreamMarkdown text={live.text} />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
