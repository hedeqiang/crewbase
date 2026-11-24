import { ReplyChip } from "../MsgExtras";
import { runShortcut } from "../../actions";
import { keyLabel } from "../../shortcuts";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowUp, Check, ChevronDown, ChevronRight, ChevronLeft, Folder, Mic, Minimize2, Monitor, Plus, MicOff, Square, Sparkles, Paperclip } from "lucide-react";
import { display, enterSends, MAC } from "../../shortcuts";
import { API, parseRef, refString, type AgentView, type ChannelView, type ModelRef, type SlashPick } from "../../api";
import { filterSlash, PickChips, pickOf, promptItems, shownOrder, skillItems, slashAt, SlashMenu, toolItems, useSlashSources, type SlashItem } from "../SlashMenu";
import { t, type Key } from "../../i18n";
import { hasModels, setModelOverride, openConnect, setState, toast, useStore, getState, setReply, newChat, openSettings } from "../../store";
import { cloudVoice, dictate, dictationSupported, primeVoice } from "../../voice";
import { addFiles, addLibraryFile, clearFiles, filesFrom, LIB_DRAG, libraryFrom, PendingChips } from "../Attachments";
import { activeModel, findProvider, modeLabel, shortModelEffort, providerName, isExhausted, until } from "../../format";
import { Avatar } from "../Avatar";
import { Popover, useMenu } from "../Popover";
import { ProviderMark } from "../ProviderMark";
import { ContextMap } from "../ContextMap";
import { ModelPicker } from "../ModelPicker";
import { SlotIcon, runCommand, useSlot } from "../UISlots";
import { shortPath, dirName } from "./util";
import { DirMenu, ModeIcon, QueueStack } from "./composerParts";

/** pastes this long (bytes) become a text attachment */
const LONG_PASTE = 32 * 1024;

function pastedFile(text: string): File {
  let ext = "txt";
  let type = "text/plain";
  const head = text.trimStart()[0];
  if (head === "{" || head === "[") {
    try {
      JSON.parse(text);
      ext = "json";
      type = "application/json";
    } catch {}
  }
  const when = new Date().toLocaleString("sv").replace(/[: ]/g, (c) => (c === ":" ? "." : " "));
  return new File([text], `${t("att.pasted")} ${when}.${ext}`, { type });
}

const fmtKB = (text: string) => {
  const n = new Blob([text]).size;
  return n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.round(n / 1024)} KB`;
};

// ---------------------------------------------------------------- composer

interface Mention {
  start: number;
  q: string;
}

/** "@xx" right before the caret (letters, digits, _ and -, like the daemon). */
function mentionAt(text: string, caret: number): Mention | null {
  const m = /(^|\s)@([\p{L}\p{N}_-]*)$/u.exec(text.slice(0, caret));
  if (!m) return null;
  return { start: caret - m[2].length - 1, q: m[2].toLowerCase() };
}

const NO_FILES: never[] = [];

export function Composer({
  sessionId,
  agent,
  channel,
  ctx,
  lastModel,
  stuck,
}: {
  sessionId: string;
  agent: AgentView;
  channel?: ChannelView;
  ctx?: { tokens: number; window: number; model?: ModelRef };
  lastModel?: ModelRef;
  /** the model this conversation fell over to, kept until a new task */
  stuck?: ModelRef;
}) {
  const agents = useStore((s) => s.agents);
  const noModels = useStore((s) => hasModels(s) === false);
  const [mention, setMention] = useState<Mention | null>(null);
  const [msel, setMsel] = useState(0);
  // the / menu (one-to-one conversations): its query, the row chosen, and
  // the skills / tool families picked for the next message
  const [slash, setSlash] = useState<string | null>(null);
  const [ssel, setSsel] = useState(0);
  const [picks, setPicks] = useState<SlashPick[]>([]);
  // a picked prompt template's arguments (for the fields on its chip), and
  // the one a send found empty
  const argDefs = useRef(new Map<string, SlashItem["args"]>());
  const [missing, setMissing] = useState<string | null>(null);
  const draft = useStore((s) => s.drafts[sessionId] ?? "");
  const override = useStore((s) => s.overrides[agent.id] ?? null);
  const hostConfirm = useStore((s) => s.hostConfirm === agent.id);
  const models = useStore((s) => s.models);
  const composerActions = useSlot("composer.action");
  const manage = useStore((s) => s.tier === "desktop");
  const plain = !channel && agent.kind === "chat";
  const viewer = useStore((s) => s.tier === "viewer");
  const [sending, setSending] = useState(false);
  const pending = useStore((s) => s.attach[sessionId]) ?? NO_FILES;
  const [dragging, setDragging] = useState(false);
  const fileIn = useRef<HTMLInputElement>(null);
  const ta = useRef<HTMLTextAreaElement>(null);
  const permM = useMenu();
  // the environment panel's folder page
  const [envDir, setEnvDir] = useState(false);
  const modelM = useMenu();
  const ctxM = useMenu();

  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 8 * 21 + 10) + "px";
  }, [draft]);

  useEffect(() => {
    ta.current?.focus();
    setPicks([]);
    setSlash(null);
  }, [sessionId]);

  const setDraft = (v: string) => setState((s) => ({ drafts: { ...s.drafts, [sessionId]: v } }));
  const overrideRef = override && !channel ? parseRef(override) : null;
  // in a channel the button is about the agent the draft is for: the
  // first member it @mentions, else the owner (who gets unaddressed messages)
  const target = useMemo(() => {
    if (!channel) return agent;
    for (const m of draft.matchAll(/(?:^|\s)@([\p{L}\p{N}_-]+)/gu)) {
      const want = m[1].toLowerCase();
      const hit = channel.members.map((id) => agents[id]).find((a) => a && (a.id.toLowerCase() === want || a.name.toLowerCase() === want));
      if (hit) return hit;
    }
    return agent;
  }, [channel, draft, agents, agent]);
  // after a failover the conversation stays on the model it switched to
  // (while that one holds), as the router does
  const kept = target === agent && stuck && !isExhausted(models, stuck) ? stuck : null;
  const shown = overrideRef ?? kept ?? activeModel(models, target) ?? (target === agent ? lastModel : undefined) ?? null;
  const prov = shown ? findProvider(models, shown.provider) : undefined;
  // the allowance behind the model in use: a gateway (magpie) reports it per
  // model, and a model with none behind it (a pay-as-you-go key, a routing
  // group) shows nothing rather than another account's figure
  const q = !shown || !prov ? undefined : prov.modelQuota ? prov.modelQuota[shown.model] : prov.quota;
  const quota = q?.known ? Math.round(q.remaining * 100) : null;
  const quotaTip = q?.known
    ? (q.windows?.length ? q.windows : [{ label: q.label || providerName(shown!.provider), remaining: q.remaining, resetsAt: q.resetsAt }])
        .map((w) => `${w.label}: ${t("set.quotaLeft", { pct: Math.round(w.remaining * 100) })}${w.resetsAt && !w.resetsAt.startsWith("0001") ? " · " + t("set.quotaResets", { time: until(w.resetsAt) }) : ""}`)
        .join("\n")
    : "";
  const pct = ctx && ctx.window > 0 ? Math.min(100, Math.round((ctx.tokens / ctx.window) * 100)) : null;

  const candidates = useMemo(() => {
    if (!channel || !mention) return [];
    return channel.members
      .map((id) => agents[id])
      .filter((a): a is AgentView => !!a && (!mention.q || a.name.toLowerCase().startsWith(mention.q) || a.id.toLowerCase().startsWith(mention.q)));
  }, [channel, mention, agents]);

  const pick = (a: AgentView) => {
    const el = ta.current;
    if (!el || !mention) return;
    const caret = el.selectionStart ?? draft.length;
    // The daemon matches names made of letters, digits, _ and -.
    const handle = /^[\p{L}\p{N}_-]+$/u.test(a.name) ? a.name : a.id;
    const next = draft.slice(0, mention.start) + "@" + handle + " " + draft.slice(caret);
    setDraft(next);
    setMention(null);
    // placed when the new text is rendered, before any further keystroke:
    // a caret set a frame later moved behind letters typed in between
    caretAfter.current = mention.start + handle.length + 2;
  };
  const caretAfter = useRef<number | null>(null);
  useLayoutEffect(() => {
    const el = ta.current;
    const pos = caretAfter.current;
    if (!el || pos === null) return;
    caretAfter.current = null;
    el.focus();
    el.setSelectionRange(pos, pos);
  }, [draft]);

  const archived = channel?.state === "archived";

  // dictation appends to the draft; interim words show live and are
  // replaced by the final transcript
  const [listening, setListening] = useState(false);
  const stopRec = useRef<null | (() => void)>(null);
  const baseDraft = useRef("");
  const toggleDictation = () => {
    if (listening) {
      stopRec.current?.();
      return;
    }
    if (!dictationSupported()) {
      toast(t("voice.unsupported"));
      return;
    }
    baseDraft.current = draft && !/\s$/.test(draft) ? draft + " " : draft;
    setListening(true);
    stopRec.current = dictate(
      (fin, interim) => setDraft(baseDraft.current + fin + interim),
      (error) => {
        setListening(false);
        stopRec.current = null;
        if (error === "denied") toast(t("voice.denied"));
        else if (error && error !== "silence" && error !== "aborted") toast(t("voice.failed", { error }));
        ta.current?.focus();
      },
    );
  };
  useEffect(() => {
    void primeVoice();
    return () => stopRec.current?.();
  }, []);

  const sendKey = (() => {
    const d = display(enterSends() ? "enter" : "mod+enter");
    return MAC ? d.join("") : d.join("+");
  })();
  const canSend = (!!draft.trim() || picks.length > 0 || pending.some((f) => f.state === "ok")) && !pending.some((f) => f.state === "up");
  const busy = !channel && (agent.status === "running" || agent.status === "waiting");

  const attach = (files: File[]) => {
    if (!files.length) return;
    if (channel) {
      toast(t("att.channel"));
      return;
    }
    addFiles(sessionId, agent.id, files);
  };

  /** While the agent works a message waits for the turn to end (排队);
   *  steer: it joins the running turn at its next step instead (插话). */
  const send = async (steer = false) => {
    const queue = !steer;
    const text = draft.trim();
    const ready = pending.filter((f) => f.state === "ok" && f.att);
    if ((!text && !ready.length && !picks.length) || sending || archived) return;
    for (const p of picks) {
      const need = p.kind === "prompt" ? (argDefs.current.get(p.name) ?? []).find((a) => a.required && !p.args?.[a.name]?.trim()) : undefined;
      if (need) {
        setMissing(p.name + "/" + need.name);
        toast(t("slash.needArg", { arg: need.name, name: p.name.slice(p.name.indexOf("/") + 1) }));
        return;
      }
    }
    setMissing(null);
    if (pending.some((f) => f.state === "up")) {
      toast(t("att.waitUpload"));
      return;
    }
    setSending(true);
    if (channel) {
      try {
        const r = await API.postChannel(sessionId, text, getState().replyTo[sessionId], picks.length ? picks : undefined);
        setDraft("");
        setReply(sessionId, null);
        setMention(null);
        setPicks([]);
        const who = agents[r.agent]?.name ?? r.agent;
        toast(r.position > 0 ? t("ch.queued", { name: who, n: r.position }) : t("ch.sentTo", { name: who }));
      } catch (e: any) {
        toast(t("chat.sendFailed", { error: e?.message ?? String(e) }));
      } finally {
        setSending(false);
        ta.current?.focus();
      }
      return;
    }
    try {
      const wasBusy = agent.status === "running" || agent.queue.length > 0;
      await API.send(agent.id, {
        text,
        queue: queue && busy ? true : undefined,
        model: override ?? undefined,
        session: sessionId === agent.session ? undefined : sessionId,
        attachments: ready.length ? ready.map((f) => ({ path: f.att!.path, name: f.att!.name })) : undefined,
        reply: getState().replyTo[sessionId],
        use: picks.length ? picks : undefined,
      });
      setDraft("");
      setReply(sessionId, null);
      clearFiles(sessionId);
      setPicks([]);
      // a queued message shows above the composer; say only what is not seen there
      if (wasBusy && busy && !queue) toast(t("steer.sent", { name: agent.name }));
    } catch (e: any) {
      toast(t("chat.sendFailed", { error: e?.message ?? String(e) }));
    } finally {
      setSending(false);
      ta.current?.focus();
    }
  };

  const setMode = async (m: string) => {
    permM.close();
    if (m === "host") {
      setState({ hostConfirm: agent.id });
      return;
    }
    try {
      await API.patchAgent(agent.id, { mode: m });
      toast(t("comp.modeSet", { name: agent.name, mode: modeLabel(m) }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  const confirmHost = async () => {
    try {
      await API.patchAgent(agent.id, { mode: "host" });
      toast(t("comp.hostEnabled", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
    setState({ hostConfirm: null });
  };

  // The pick becomes the first model of the agent the message is for (kept
  // by the daemon: the panel, other devices and a reload agree; the rest of
  // its order stays as fallbacks). 按模型顺序 clears a one-to-one override.
  const setOverride = (r: ModelRef | null) => {
    if (!r) {
      setModelOverride(agent.id, null);
      return;
    }
    if (!channel) setModelOverride(agent.id, null);
    const rest = (target.models ?? []).filter((m) => !(m.provider === r.provider && m.model === r.model));
    void API.patchAgent(target.id, { modelRefs: [refString(r), ...rest.map((m) => refString(m))], session: sessionId })
      .then(() => toast(t("comp.modelSet", { name: target.name, model: shortModelEffort(models, r)?.name ?? r.model })))
      .catch((e: any) => toast(e?.message ?? String(e)));
  };

  const mode = agent.effectiveMode || agent.mode || "sandbox";

  // ---- the / menu
  const slashOpen = slash !== null && !archived;
  // in a channel: what the agent the message goes to can use
  const sources = useSlashSources(target.id, slashOpen);
  const commands: SlashItem[] = slashOpen
    ? [
        { key: "cmd:new", kind: "cmd", name: "new", desc: t("slash.cmd.new"), icon: <Plus size={14} />, run: () => void newChat() },
        { key: "cmd:model", kind: "cmd", name: "model", desc: t("slash.cmd.model"), icon: <Sparkles size={14} />, run: () => setTimeout(() => modelM.setOpen(true), 0) },
        ...(!plain && !channel && manage ? [{ key: "cmd:mode", kind: "cmd" as const, name: "mode", desc: t("slash.cmd.mode"), icon: <ModeIcon mode={mode} size={14} />, run: () => setTimeout(() => permM.setOpen(true), 0) }] : []),
        ...(channel ? [] : [{ key: "cmd:attach", kind: "cmd" as const, name: "attach", desc: t("slash.cmd.attach"), icon: <Paperclip size={14} />, run: () => fileIn.current?.click() }]),
        ...(!channel && ctx && ctx.tokens > 0 ? [{ key: "cmd:compact", kind: "cmd" as const, name: "compact", desc: t("slash.cmd.compact"), icon: <Minimize2 size={14} />, run: () => void API.compact(agent.id, sessionId).then(() => toast(t("ctx.compacted", { name: agent.name })), (e) => toast(e?.message ?? String(e))) }] : []),
        ...(busy ? [{ key: "cmd:stop", kind: "cmd" as const, name: "stop", desc: t("slash.cmd.stop"), icon: <Square size={14} />, run: () => void API.action(agent.id, "stop").catch((e) => toast(e?.message ?? String(e))) }] : []),
        ...composerActions.map((c) => ({ key: "slot:" + c.key, kind: "cmd" as const, name: c.key.replace(/^.*[/:]/, ""), desc: c.desc || c.title, icon: <SlotIcon name={c.icon} />, run: () => void runCommand(c, sessionId) })),
      ]
    : [];
  const slashItems = slashOpen ? shownOrder(filterSlash([...skillItems(sources), ...promptItems(sources), ...commands, ...toolItems(sources)], slash ?? "").filter((it) => !picks.some((p) => it.key === p.kind + ":" + p.name)).slice(0, 40)) : [];
  const pickSlash = (it: SlashItem) => {
    const el = ta.current;
    const caret = el?.selectionStart ?? draft.length;
    // the "/query" goes; what follows it stays
    const rest = draft.slice(caret).replace(/^\s+/, "");
    setDraft(rest);
    setSlash(null);
    caretAfter.current = 0;
    if (it.kind === "cmd") it.run?.();
    else {
      const p = pickOf(it);
      if (!p) return;
      if (it.kind === "prompt") argDefs.current.set(p.name, it.args);
      setPicks((l) => (l.some((x) => x.kind === p.kind && x.name === p.name) ? l : [...l, p]));
      // a template with arguments: its first field
      if (it.args?.length) setTimeout(() => document.querySelector<HTMLInputElement>(".pickchip.withargs:last-child .pkarg input")?.focus(), 30);
    }
  };

  if (viewer)
    return (
      <div className="cwrap">
        <p className="tiernote viewnote">{t("team.viewerComposer")}</p>
      </div>
    );
  return (
    <div className="cwrap">
      {hostConfirm && (
        <div className="confirmbar">
          <p>
            <b>{t("comp.hostConfirm", { name: agent.name })}</b> {t("comp.hostConfirmDesc")}
          </p>
          <button className="btn" type="button" onClick={() => setState({ hostConfirm: null })}>
            {t("comp.hostKeep")}
          </button>
          <button className="btn dng" type="button" onClick={confirmHost}>
            {t("comp.hostOk")}
          </button>
        </div>
      )}
      {noModels && !archived && (
        <div className="confirmbar nomodel">
          <p>
            <b>{t("conn.none")}</b> {t("conn.noneDesc")}
          </p>
          <button className="btn pri" type="button" onClick={openConnect}>
            {t("conn.connect")}
          </button>
        </div>
      )}
      {!channel && <QueueStack agent={agent} session={sessionId} busy={busy} />}
      <form
        className={"composer" + (dragging ? " drop" : "")}
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
        onDragOver={(e) => {
          const types = Array.from(e.dataTransfer.types);
          if (archived || (!types.includes("Files") && !types.includes(LIB_DRAG))) return;
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={(e) => {
          if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false);
        }}
        onDrop={(e) => {
          const lib = libraryFrom(e.dataTransfer);
          if (lib) {
            e.preventDefault();
            setDragging(false);
            if (channel) toast(t("att.channel"));
            else addLibraryFile(sessionId, agent.id, lib);
            return;
          }
          if (!Array.from(e.dataTransfer.types).includes("Files")) return;
          e.preventDefault();
          setDragging(false);
          attach(filesFrom(e.dataTransfer));
        }}
      >
        {dragging && <div className="dropmask">{t("att.drop")}</div>}
        <ReplyChip session={sessionId} />
        <PendingChips session={sessionId} files={pending} />
        <PickChips
          picks={picks}
          missing={missing}
          argsOf={(p) => argDefs.current.get(p.name)}
          onRemove={(p) => setPicks((l) => l.filter((x) => x !== p))}
          onArgs={(p, args) => {
            setMissing(null);
            setPicks((l) => l.map((x) => (x === p ? { ...x, args } : x)));
          }}
        />
        {slashOpen && (
          <SlashMenu
            items={slashItems}
            query={slash ?? ""}
            forWho={channel ? target.name : undefined}
            sel={slashItems.length ? ssel % slashItems.length : 0}
            loading={!sources}
            onHover={setSsel}
            onPick={pickSlash}
            empty={
              <>
                {t("slash.none", { q: slash ?? "" })}{" "}
                <button type="button" className="linkbtn" onClick={() => (setSlash(null), openSettings("market"))}>
                  {t("slash.getSkills")}
                </button>
              </>
            }
          />
        )}
        {candidates.length > 0 && (
          <div className="mention" role="listbox" aria-label={t("ch.mention")}>
            <div className="mlbl">{t("ch.mention")}</div>
            {candidates.map((a, i) => (
              <button
                key={a.id}
                type="button"
                role="option"
                aria-selected={i === msel % candidates.length}
                className="mi"
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(a);
                }}
              >
                <Avatar agent={a} size={22} status />
                <span className="g">
                  <b>{a.name}</b>
                  <span>
                    {a.role || a.id}
                    {a.id === channel?.owner ? ` · ${t("ch.owner")}` : ""}
                    {a.queue.length ? ` · ${t("ch.queueLen", { n: a.queue.length })}` : ""}
                  </span>
                </span>
              </button>
            ))}
          </div>
        )}
        <textarea
          ref={ta}
          rows={1}
          value={draft}
          disabled={archived}
          placeholder={archived ? t("ch.archivedPh") : channel ? t("chat.placeholderChannel", { name: channel.title || channel.id }) : agent.kind === "chat" ? t("chat.chatPh") : t("chat.placeholder", { name: agent.name })}
          aria-label={channel ? t("chat.placeholderChannel", { name: channel.title || channel.id }) : agent.kind === "chat" ? t("chat.chatPh") : t("chat.placeholder", { name: agent.name })}
          onChange={(e) => {
            setDraft(e.target.value);
            {
              const q = slashAt(e.target.value, e.target.selectionStart ?? e.target.value.length);
              setSlash(q);
              if (q !== slash) setSsel(0);
            }
            if (channel) {
              const m = mentionAt(e.target.value, e.target.selectionStart ?? e.target.value.length);
              setMention(m);
              setMsel(0);
            }
          }}
          onBlur={() => {
            setMention(null);
            setSlash(null);
          }}
          onPaste={(e) => {
            const files = filesFrom(e.clipboardData);
            if (files.length) {
              e.preventDefault();
              attach(files);
              return;
            }
            // a long paste (a log, a document, a file's code) is sent as a
            // text file instead of filling the box
            const text = e.clipboardData.getData("text/plain");
            if (!channel && text.length >= LONG_PASTE && new Blob([text]).size >= LONG_PASTE) {
              e.preventDefault();
              attach([pastedFile(text)]);
              toast(t("att.longPaste", { size: fmtKB(text) }));
            }
          }}
          onKeyDown={(e) => {
            if (slashOpen) {
              const n = slashItems.length;
              if ((e.key === "ArrowDown" || e.key === "ArrowUp") && n > 0) {
                e.preventDefault();
                setSsel((i) => (i + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
                return;
              }
              if ((e.key === "Enter" || e.key === "Tab") && n > 0 && !e.nativeEvent.isComposing && !e.shiftKey) {
                e.preventDefault();
                pickSlash(slashItems[ssel % n]);
                return;
              }
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                setSlash(null);
                return;
              }
            }
            // Backspace at the very start takes back the last pick
            if (e.key === "Backspace" && picks.length && e.currentTarget.selectionStart === 0 && e.currentTarget.selectionEnd === 0) {
              e.preventDefault();
              setPicks((l) => l.slice(0, -1));
              return;
            }
            if (candidates.length > 0) {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                setMsel((i) => (i + (e.key === "ArrowDown" ? 1 : candidates.length - 1)) % candidates.length);
                return;
              }
              if (e.key === "Enter" || e.key === "Tab") {
                e.preventDefault();
                pick(candidates[msel % candidates.length]);
                return;
              }
              if (e.key === "Escape") {
                e.preventDefault();
                e.stopPropagation();
                setMention(null);
                return;
              }
            }
            if (e.key === "Escape" && getState().replyTo[sessionId]) {
              e.preventDefault();
              e.stopPropagation();
              setReply(sessionId, null);
              return;
            }
            // ⌘Enter sends; with 「Enter 发送」 on, plain Enter sends and
            // Shift+Enter starts a new line
            // while the agent works a message queues; ⌥Enter joins the running turn (插话)
            if (e.key === "Enter" && !e.nativeEvent.isComposing && busy && e.altKey && !e.shiftKey) {
              e.preventDefault();
              void send(true);
              return;
            }
            if (e.key === "Enter" && !e.nativeEvent.isComposing && ((e.metaKey || e.ctrlKey) || (enterSends() && !e.shiftKey && !e.altKey))) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <div className="crow">
          <button
            className="cbtn round"
            type="button"
            aria-label={t("chat.attach")}
            title={channel ? t("att.channel") : t("chat.attach")}
            disabled={archived}
            onClick={() => (channel ? toast(t("att.channel")) : fileIn.current?.click())}
          >
            <Plus size={16} />
          </button>
          <input
            ref={fileIn}
            type="file"
            hidden
            multiple
            accept="image/*,.pdf,.doc,.docx,.ppt,.pptx,.xls,.xlsx,.csv,.txt,.md,.json,.yaml,.yml,.xml,.html,.log,.go,.py,.js,.ts,.tsx,.java,.rs,.c,.cpp,.rb,.php,.sh,.sql"
            onChange={(e) => {
              attach(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
          {/* a blank chat runs nothing: no host, run mode or folder to choose */}
          {!plain && (<>
          {/* where and how it runs, in one chip: permission · folder */}
          <button
            className={"cbtn envchip" + (mode === "host" ? " danger" : "")}
            type="button"
            ref={permM.ref}
            onClick={permM.toggle}
            aria-haspopup="dialog"
            disabled={!manage}
            title={manage ? t("env.title", { name: agent.name }) : t("tier.mobileMode")}
          >
            <ModeIcon mode={mode} size={15} />
            <span className="lbl">
              {modeLabel(mode)}
              {!channel && <span className="envdir"> · {dirName(agent.workspace, !!agent.sharedWorkspace)}</span>}
            </span>
            <ChevronDown size={14} />
          </button>
          </>)}
          {composerActions.map((c) => (
            <button key={c.key} className="cbtn" type="button" title={c.desc || c.title} onClick={() => void runCommand(c, sessionId)}>
              <SlotIcon name={c.icon} />
              <span className="lbl">{c.title}</span>
            </button>
          ))}
          <span className="sp" />
          <button
            className={"cbtn cmodel" + (override && !channel ? " ov" : "")}
            type="button"
            ref={modelM.ref}
            onClick={modelM.toggle}
            title={channel ? t("ch.modelOf", { name: target.name }) : t("comp.modelTitle")}
            aria-haspopup="menu"
          >
            {shown ? (
              <>
                <ProviderMark provider={shown.provider} model={shown.model} size={12} />
                <span className="lbl">{shortModelEffort(models, shown).name}</span>
                {shortModelEffort(models, shown).effort && <span className="eff">{shortModelEffort(models, shown).effort}</span>}
              </>
            ) : (
              <span className="lbl">{t("na.modelNone")}</span>
            )}
            <ChevronDown size={14} />
          </button>
          <button
            className={"cbtn round ghost" + (listening ? " rec" : "")}
            type="button"
            aria-label={listening ? t("voice.stopDictation") : t("chat.voice")}
            aria-pressed={listening}
            title={!dictationSupported() ? t("voice.unsupported") : listening ? t("voice.stopDictation") : cloudVoice() ? t("voice.cloud") : t("chat.voice")}
            disabled={archived}
            onClick={toggleDictation}
          >
            {listening ? <MicOff size={15} /> : <Mic size={15} />}
          </button>
          {busy && !canSend ? (
            // the agent is working and nothing is typed: the button stops it
            // (typed text still sends: it joins the running turn)
            <button className="send on stop" type="button" onClick={() => runShortcut("stop")} aria-label={t("keys.stop")} title={t("keys.stop") + " · " + keyLabel("stop")}>
              <Square size={12} fill="currentColor" />
            </button>
          ) : (
            <>
              {busy && canSend ? (
                // while it works the message queues; ⌥Enter (or 插话 on a queued one) joins the turn
                <button
                  className="send on"
                  type="submit"
                  disabled={sending}
                  aria-label={t("steer.queueTitle", { key: sendKey })}
                  title={t("steer.queueTitle", { key: sendKey }) + "\n" + t("steer.sendTitle", { key: display("alt+enter").join("") })}
                >
                  <ArrowUp size={16} />
                </button>
              ) : (
                <button
                  className={"send" + (canSend ? " on" : "")}
                  type="submit"
                  disabled={!canSend || sending}
                  aria-label={t("chat.send", { key: sendKey })}
                  title={t("chat.send", { key: sendKey })}
                >
                  <ArrowUp size={16} />
                </button>
              )}
            </>
          )}
        </div>
        <div className="cfoot">
        {/* a plain chat has no tools: say so before a link is sent to it */}
        {plain && /https?:\/\/\S/.test(draft) ? <span className="small muted cnote">{t("chat.plainNoLinks")}</span> : <span className="sp" />}
        {shown && quota !== null && (
        <span className={"quota" + (quota <= 10 ? " warnq" : "")} title={quotaTip}>
        <ProviderMark provider={shown.provider} model={shown.model} size={11} />
        {t("comp.quota", { model: q?.label ? q.label.replace(/\s*\([^)]*\)/, "") : providerName(shown.provider), pct: quota })}
        </span>
        )}
        <button
        className="ctxbtn"
        type="button"
        ref={ctxM.ref}
        onClick={ctxM.toggle}
        aria-haspopup="menu"
        title={pct === null ? t("comp.contextNone") : t("comp.context", { pct })}
        aria-label={pct === null ? t("comp.contextNone") : t("comp.context", { pct })}
        >
        <i className={"ring" + (pct !== null && pct >= 80 ? " hi" : "")} style={{ ["--p" as any]: pct ?? 0 }} />
        {pct ? t("comp.context", { pct }) : null}
        </button>
        </div>
      </form>

      <Popover anchor={permM.anchor} open={permM.open} onClose={() => (permM.close(), setEnvDir(false))} placement="top" width={340}>
        {envDir ? (
          <>
            <button className="mi flback" type="button" onClick={() => setEnvDir(false)}>
              <ChevronLeft size={14} />
              <span className="g">{t("env.title", { name: agent.name })}</span>
            </button>
            <DirMenu agent={agent} onDone={() => (permM.close(), setEnvDir(false))} />
          </>
        ) : (
          <>
            <div className="mlbl">{t("env.where")}</div>
            <div className="mi static">
              <Monitor size={16} />
              <span className="g">
                {t("comp.hostLocal")}
                <span>
                  <i className="hdot on" />
                  {t("comp.hostLocalDesc")}
                </span>
              </span>
            </div>
            <hr />
            <div className="mlbl">{t("env.perm")}</div>
            {(["read-only", "sandbox", "host"] as const).map((m) => (
              <button key={m} className={"mi" + (m === "host" ? " envhost" : "")} type="button" onClick={() => setMode(m)}>
                <ModeIcon mode={m} size={16} />
                <span className="g">
                  {modeLabel(m)}
                  <span>{t(`mode.${m}.desc` as Key)}</span>
                </span>
                <span className="ck">{mode === m && <Check size={14} />}</span>
              </button>
            ))}
            {!channel && (
              <>
                <hr />
                <div className="mlbl">{t("env.folder")}</div>
                <button className="mi" type="button" onClick={() => setEnvDir(true)}>
                  <Folder size={16} />
                  <span className="g">
                    {dirName(agent.workspace, !!agent.sharedWorkspace)}
                    <span className="mono">{shortPath(agent.workspace)}</span>
                  </span>
                  <ChevronRight size={14} className="muted" />
                </button>
              </>
            )}
            <p className="small" style={{ margin: "6px 9px 6px" }}>
              {t("comp.permRules")}
            </p>
          </>
        )}
      </Popover>

      <Popover anchor={modelM.anchor} open={modelM.open} onClose={modelM.close} placement="top" align="end" width={260}>
        <ModelPicker models={models} agent={target} override={overrideRef} shown={shown} setOverride={setOverride} close={modelM.close} />
      </Popover>

      {ctxM.open && <ContextMap agent={agent} sessionId={sessionId} channel={!!channel} ctx={ctx} onClose={ctxM.close} />}
    </div>
  );
}
