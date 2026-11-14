import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  CircleX,
  Folder,
  Hand,
  Mic,
  Minimize2,
  Monitor,
  Plus,
  Shield,
  Wrench,
  MessageSquarePlus,
} from "lucide-react";
import { API, parseRef, refString, type AgentView, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { argsFull, argsPreview, project, type FeedItem, type Step } from "../projection";
import { loadSession, setState, toast, useStore, sessionAgent } from "../store";
import {
  activeModel,
  ago,
  allModels,
  dayLabel,
  effortLabel,
  findProvider,
  hhmm,
  isExhausted,
  kfmt,
  modeLabel,
  modelLabel,
  sameRef,
  until,
} from "../format";
import { Avatar } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { Markdown } from "./Markdown";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";

export function Conversation() {
  const current = useStore((s) => s.current);
  const sessions = useStore((s) => s.sessions);
  const agents = useStore((s) => s.agents);
  useEffect(() => {
    if (current) void loadSession(current);
  }, [current]);
  const agent = useMemo(() => sessionAgent(current), [current, sessions, agents]);

  if (!current || !agent)
    return (
      <section className="main">
        <div className="emptyv">
          <div className="chico">
            <MessageSquarePlus />
          </div>
          <b>{t("chat.noAgent")}</b>
          <span className="small">{t("chat.noAgentDesc")}</span>
        </div>
      </section>
    );
  return <ConversationView key={current} sessionId={current} agent={agent} />;
}

function ConversationView({ sessionId, agent }: { sessionId: string; agent: AgentView }) {
  const events = useStore((s) => s.events[sessionId]);
  const err = useStore((s) => s.loadErr[sessionId]);
  const proj = useMemo(() => project(events ?? []), [events]);
  const ctx = proj.context[agent.id];
  return (
    <section className="main">
      <Feed sessionId={sessionId} agent={agent} items={proj.items} loading={!events} error={err} />
      <Composer sessionId={sessionId} agent={agent} ctx={ctx} lastModel={proj.lastModel[agent.id]} />
    </section>
  );
}

// ---------------------------------------------------------------- feed

function Feed({ sessionId, agent, items, loading, error }: { sessionId: string; agent: AgentView; items: FeedItem[]; loading: boolean; error?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  const streams = useStore((s) => s.streams);
  const agents = useStore((s) => s.agents);
  const live = Object.entries(streams).filter(([k]) => k.startsWith(sessionId + "|"));
  const waitingApproval = useStore((s) => s.approvals.some((a) => a.agent === agent.id && a.session === sessionId));
  const typing = agent.status === "running" && !waitingApproval && !live.some(([k, v]) => k.endsWith("|" + agent.id) && v.text);

  const onScroll = () => {
    const el = ref.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  });

  return (
    <div className="feed" ref={ref} onScroll={onScroll}>
      {error && (
        <div className="sys err">
          <span className="dot err" />
          {t("chat.loadFailed", { error })}
          <button className="linkbtn" type="button" onClick={() => loadSession(sessionId)}>
            {t("common.retry")}
          </button>
        </div>
      )}
      {!loading && items.length === 0 && (
        <div className="feedintro">
          <Avatar agent={agent} size={56} />
          <b>{t("chat.empty", { name: agent.name })}</b>
          <span className="small">{t("chat.emptyDesc", { dir: agent.workspace })}</span>
        </div>
      )}
      {items.map((it) => (
        <FeedRow key={it.id} it={it} agents={agents} />
      ))}
      {live.map(([k, v]) => {
        const a = agents[k.split("|")[1]];
        if (!v.text && !v.thinking) return null;
        return (
          <div className="m-agent live" key={k}>
            <Avatar agent={a} size={30} />
            <div style={{ minWidth: 0 }}>
              <div className="mh">
                <b>{a?.name ?? k.split("|")[1]}</b>
                <span className="t">{t("status.running")}</span>
              </div>
              {v.thinking && !v.text && <div className="thinking small">{v.thinking.slice(-400)}</div>}
              {v.text && (
                <div className="mt">
                  <Markdown text={v.text} />
                  <span className="caret" />
                </div>
              )}
            </div>
          </div>
        );
      })}
      {typing && (
        <div className="typing">
          <Avatar agent={agent} size={24} />
          <span>{agent.detail || t("chat.thinking", { name: agent.name })}</span>
          <span className="dots">
            <i />
            <i />
            <i />
          </span>
        </div>
      )}
    </div>
  );
}

function FeedRow({ it, agents }: { it: FeedItem; agents: Record<string, AgentView> }) {
  const models = useStore((s) => s.models);
  switch (it.k) {
    case "time":
      return <div className="tmark">{dayLabel(it.time)}</div>;
    case "user":
      return (
        <div className="m-user">
          <div className="bubble">
            {it.from && <span className="from">{it.from}</span>}
            {it.text}
          </div>
        </div>
      );
    case "agent":
      return <AgentMsg it={it} agent={agents[it.agent]} />;
    case "steps":
      return <Steps steps={it.steps} />;
    case "approval":
      return <ApprovalCard it={it} agent={agents[it.req.agent]} />;
    case "switch": {
      const a = agents[it.agent];
      const reason = t((`chat.reason.${it.reason}` as Key) in REASONS ? (`chat.reason.${it.reason}` as Key) : "chat.reason.other");
      return (
        <div className="sys" title={it.detail}>
          <span className="dot err" />
          <span>
            {t("chat.modelSwitched", {
              agent: a?.name ?? it.agent,
              from: it.from ? modelLabel(models, it.from) : "?",
              to: it.to ? modelLabel(models, it.to) : "?",
              reason,
            })}
          </span>
        </div>
      );
    }
    case "mode": {
      const a = agents[it.agent];
      return (
        <div className="sys">
          {it.to === "host" ? <Monitor size={14} /> : <Shield size={14} />}
          <span>
            {t("chat.modeChanged", { agent: a?.name ?? it.agent, mode: modeLabel(it.to) })}
            {it.until ? t("chat.modeUntil", { time: until(it.until) }) : ""}
          </span>
        </div>
      );
    }
    case "compact":
      return <Compact it={it} />;
    case "sys":
      return (
        <div className={"sys " + (it.tone === "error" ? "err" : it.tone)}>
          {it.tone === "error" && <span className="dot err" />}
          <span>{it.key ? t(it.key as Key, it.vars) : it.text}</span>
        </div>
      );
  }
}

const REASONS: Record<string, true> = {
  "chat.reason.quota_exhausted": true,
  "chat.reason.rate_limited": true,
  "chat.reason.unavailable": true,
};

function AgentMsg({ it, agent }: { it: Extract<FeedItem, { k: "agent" }>; agent?: AgentView }) {
  const models = useStore((s) => s.models);
  const [showThink, setShowThink] = useState(false);
  return (
    <div className="m-agent">
      <Avatar agent={agent} size={30} />
      <div style={{ minWidth: 0 }}>
        <div className="mh">
          <b>{agent?.name ?? it.agent}</b>
          <span className="t">{hhmm(it.time)}</span>
          {it.model && (
            <span className="via">
              {t("chat.via", { model: modelLabel(models, it.model) })}
              {it.model.effort ? ` · ${effortLabel(it.model.effort)}` : ""}
            </span>
          )}
        </div>
        {it.thinking && (
          <button className="row-btn think" type="button" aria-expanded={showThink} onClick={() => setShowThink(!showThink)}>
            {t("chat.thoughts")}
            <ChevronRight size={14} className="ch" />
          </button>
        )}
        {showThink && <div className="thinking small">{it.thinking}</div>}
        {it.text && (
          <div className="mt">
            <Markdown text={it.text} />
          </div>
        )}
      </div>
    </div>
  );
}

function Steps({ steps }: { steps: Step[] }) {
  const [open, setOpen] = useState(false);
  const running = steps.some((s) => !s.result && !s.awaiting);
  const awaiting = steps.some((s) => s.awaiting);
  const errors = steps.filter((s) => s.result?.isError).length;
  return (
    <div className="stepsw">
      <button className="row-btn" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {running ? <span className="spin" /> : <Wrench size={14} className="muted" />}
        {running ? t("chat.stepsRunning", { n: steps.length }) : t("chat.steps", { n: steps.length })}
        {awaiting && <span className="pill amber">{t("appr.waiting")}</span>}
        {errors > 0 && <span className="pill red">{t("chat.toolError")} {errors}</span>}
        <ChevronRight size={14} className="ch" />
      </button>
      {open && (
        <div className="stepsx">
          {steps.map((s) => (
            <StepRow key={s.call.id} s={s} />
          ))}
        </div>
      )}
    </div>
  );
}

function StepRow({ s }: { s: Step }) {
  const [open, setOpen] = useState(false);
  const pv = argsPreview(s.call.args);
  return (
    <div className="step">
      <button className="stephead" type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {s.awaiting ? <Hand size={13} className="waitc" /> : !s.result ? <span className="spin sm" /> : s.result.isError ? <CircleX size={13} className="errc" /> : <Check size={13} className="okc" />}
        <b className="mono">{s.call.name}</b>
        <span className="mono pv">{pv}</span>
      </button>
      {open && (
        <div className="stepbody">
          <pre className="codeblk">{argsFull(s.call.args)}</pre>
          {s.result && <pre className={"codeblk" + (s.result.isError ? " errblk" : "")}>{clip(s.result.content)}</pre>}
        </div>
      )}
    </div>
  );
}

function clip(s: string, n = 6000) {
  return s.length > n ? s.slice(0, n) + `\n… (${s.length - n} more)` : s;
}

function ApprovalCard({ it, agent }: { it: Extract<FeedItem, { k: "approval" }>; agent?: AgentView }) {
  const pending = useStore((s) => s.approvals.some((a) => a.callId === it.req.callId));
  const req = useStore((s) => s.approvals.find((a) => a.callId === it.req.callId)) ?? it.req;
  const name = agent?.name ?? it.req.agent;
  const summary = it.req.summary || it.req.tool;
  if (it.decided) {
    const d = it.decided;
    const by = d.by && d.by !== "desktop" ? d.by + " " : t("common.you");
    const key: Key =
      d.outcome === "allowed-once"
        ? d.remember
          ? "appr.approvedRemember"
          : "appr.approved"
        : d.outcome === "rejected"
          ? d.message
            ? "appr.rejectedWith"
            : "appr.rejected"
          : d.outcome === "cancelled"
            ? "appr.cancelled"
            : "appr.unavailable";
    return (
      <div className={"sys" + (d.outcome === "allowed-once" ? "" : " muted")}>
        {d.outcome === "allowed-once" ? <Check size={14} /> : <CircleX size={14} />}
        <span>{t(key, { by, summary, message: d.message ?? "" })}</span>
      </div>
    );
  }
  const cmd = it.call ? argsPreview(it.call.args) : "";
  return (
    <div className={"qcard" + (pending ? " wait" : "")}>
      <h4>{t("appr.wants", { agent: name, summary })}</h4>
      <div className="qs">{t("appr.rule", { reason: it.req.reason || it.req.category, wait: ago(it.req.asked || it.time) })}</div>
      {cmd && <code className="cmd">{cmd}</code>}
      <div className="small">
        {it.req.tool}
        {it.req.category ? ` · ${it.req.category}` : ""}
      </div>
      {pending ? <ApprovalActions req={req} /> : <p className="small" style={{ margin: "10px 0 0" }}>{t("appr.expired")}</p>}
    </div>
  );
}

function Compact({ it }: { it: Extract<FeedItem, { k: "compact" }> }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <div className="sys">
        <Minimize2 size={14} />
        <span>{t("chat.compacted", { before: kfmt(it.before), after: kfmt(it.after) })}</span>
        {it.summary && (
          <button className="linkbtn" type="button" onClick={() => setOpen(!open)}>
            {t("chat.compactSummary")}
          </button>
        )}
      </div>
      {open && (
        <div className="brief">
          <Markdown text={it.summary} />
        </div>
      )}
    </>
  );
}

// ---------------------------------------------------------------- composer

function Composer({ sessionId, agent, ctx, lastModel }: { sessionId: string; agent: AgentView; ctx?: { tokens: number; window: number; model?: ModelRef }; lastModel?: ModelRef }) {
  const draft = useStore((s) => s.drafts[sessionId] ?? "");
  const override = useStore((s) => s.overrides[agent.id] ?? null);
  const hostConfirm = useStore((s) => s.hostConfirm === agent.id);
  const models = useStore((s) => s.models);
  const [sending, setSending] = useState(false);
  const ta = useRef<HTMLTextAreaElement>(null);
  const hostM = useMenu();
  const permM = useMenu();
  const modelM = useMenu();
  const ctxM = useMenu();

  useLayoutEffect(() => {
    const el = ta.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 220) + "px";
  }, [draft]);

  useEffect(() => {
    ta.current?.focus();
  }, [sessionId]);

  const setDraft = (v: string) => setState((s) => ({ drafts: { ...s.drafts, [sessionId]: v } }));
  const overrideRef = override ? parseRef(override) : null;
  const shown = overrideRef ?? activeModel(models, agent) ?? lastModel ?? null;
  const prov = shown ? findProvider(models, shown.provider) : undefined;
  const quota = prov?.quota?.known ? Math.round(prov.quota.remaining * 100) : null;
  const pct = ctx && ctx.window > 0 ? Math.min(100, Math.round((ctx.tokens / ctx.window) * 100)) : null;

  const send = async () => {
    const text = draft.trim();
    if (!text || sending) return;
    setSending(true);
    try {
      const wasBusy = agent.status === "running" || agent.queue.length > 0;
      await API.send(agent.id, { text, model: override ?? undefined, session: sessionId === agent.session ? undefined : sessionId });
      setDraft("");
      if (wasBusy) toast(t("chat.queued", { n: agent.queue.length + 1 }));
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

  const setOverride = (r: ModelRef | null) => setState((s) => ({ overrides: { ...s.overrides, [agent.id]: r ? refString(r) : null } }));

  const order = agent.models ?? [];
  const others = allModels(models).filter((m) => !order.some((r) => sameRef(r, m.ref)));
  const mode = agent.effectiveMode || agent.mode || "sandbox";

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
      <form
        className="composer"
        onSubmit={(e) => {
          e.preventDefault();
          void send();
        }}
      >
        <textarea
          ref={ta}
          rows={1}
          value={draft}
          placeholder={t("chat.placeholder", { name: agent.name })}
          aria-label={t("chat.placeholder", { name: agent.name })}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <div className="crow">
          <button className="cbtn round" type="button" aria-label={t("chat.attach")} title={t("chat.attach")} onClick={() => toast(t("chat.attach"))}>
            <Plus size={16} />
          </button>
          <button className="cbtn" type="button" ref={hostM.ref} onClick={hostM.toggle} title={t("comp.host", { name: agent.name })} aria-haspopup="menu">
            <Monitor size={15} />
            <span className="lbl">{t("comp.hostLocal")}</span>
            <ChevronDown size={14} />
          </button>
          <button className={"cbtn" + (mode === "host" ? " danger" : "")} type="button" ref={permM.ref} onClick={permM.toggle} aria-haspopup="menu">
            {mode === "host" ? <Monitor size={15} /> : <Hand size={15} />}
            <span className="lbl">{modeLabel(mode)}</span>
            <ChevronDown size={14} />
          </button>
          <span className="sp" />
          <button className={"cbtn cmodel" + (override ? " ov" : "")} type="button" ref={modelM.ref} onClick={modelM.toggle} title={t("comp.modelTitle")} aria-haspopup="menu">
            {shown ? (
              <>
                <ProviderMark provider={shown.provider} model={shown.model} size={12} />
                <span className="lbl">{modelLabel(models, shown)}</span>
                {shown.effort && <span className="eff">· {effortLabel(shown.effort)}</span>}
              </>
            ) : (
              <span className="lbl">{t("na.modelNone")}</span>
            )}
            <ChevronDown size={14} />
          </button>
          <button className="cbtn round" type="button" aria-label={t("chat.voice")} title={t("chat.voice")} onClick={() => toast(t("chat.voice"))}>
            <Mic size={15} />
          </button>
          <button className="send" type="submit" disabled={!draft.trim() || sending} aria-label={t("chat.send")} title={t("chat.send")}>
            <ArrowUp size={16} />
          </button>
        </div>
      </form>
      <div className="cfoot">
        <button
          className="hostlink"
          type="button"
          title={t("comp.workdir", { dir: agent.workspace })}
          onClick={() => {
            void navigator.clipboard?.writeText(agent.workspace).then(() => toast(t("comp.copied")), () => {});
          }}
        >
          <Folder size={13} />
          <span className="path">{shortPath(agent.workspace)}</span>
        </button>
        <span className="sp" />
        {shown && quota !== null && (
          <span className={quota <= 10 ? "warnq" : ""}>
            <ProviderMark provider={shown.provider} model={shown.model} size={11} />
            {t("comp.quota", { model: modelLabel(models, shown), pct: quota })}
          </span>
        )}
        <button className="ctxbtn" type="button" ref={ctxM.ref} onClick={ctxM.toggle} aria-haspopup="menu">
          <i className={"ring" + (pct !== null && pct >= 80 ? " hi" : "")} style={{ ["--p" as any]: pct ?? 0 }} />
          {pct === null ? t("comp.contextNone") : t("comp.context", { pct })}
        </button>
      </div>

      <Popover anchor={hostM.anchor} open={hostM.open} onClose={hostM.close} placement="top">
        <div className="mlbl">{t("comp.host", { name: agent.name })}</div>
        <button className="mi" type="button" onClick={hostM.close}>
          <Monitor size={16} />
          <span className="g">
            {t("comp.hostLocal")}
            <span>
              <i className="hdot on" />
              {t("comp.hostLocalDesc")}
            </span>
          </span>
          <span className="ck">
            <Check size={14} />
          </span>
        </button>
        <hr />
        <button className="mi" type="button" disabled>
          <Plus size={16} />
          <span className="g">
            {t("comp.hostAdd")}
            <span>{t("common.soon")}</span>
          </span>
        </button>
      </Popover>

      <Popover anchor={permM.anchor} open={permM.open} onClose={permM.close} placement="top" width={300}>
        <div className="mlbl">{t("comp.permTitle", { name: agent.name, host: t("comp.hostLocal") })}</div>
        {(["read-only", "sandbox", "host"] as const).map((m) => (
          <button key={m} className="mi" type="button" onClick={() => setMode(m)}>
            {m === "host" ? <Monitor size={16} /> : <Shield size={16} />}
            <span className="g">
              {modeLabel(m)}
              <span>{t(`mode.${m}.desc` as Key)}</span>
            </span>
            <span className="ck">{mode === m && <Check size={14} />}</span>
          </button>
        ))}
        <hr />
        <p className="small" style={{ margin: "4px 9px 6px" }}>
          {t("comp.permRules")}
        </p>
      </Popover>

      <Popover anchor={modelM.anchor} open={modelM.open} onClose={modelM.close} placement="top" align="end" width={320}>
        <div className="mlbl">{t("comp.modelTitle")}</div>
        <button
          className="mi"
          type="button"
          onClick={() => {
            setOverride(null);
            modelM.close();
          }}
        >
          <Check size={16} style={{ opacity: override ? 0 : 1 }} />
          <span className="g">
            {t("comp.modelAuto")}
            <span>{t("comp.modelAutoDesc")}</span>
          </span>
        </button>
        {order.length > 0 && <div className="mlbl">{t("comp.modelOrder", { name: agent.name })}</div>}
        {order.map((r, i) => {
          const ex = isExhausted(models, r);
          return (
            <button
              key={refString(r)}
              className="mi"
              type="button"
              onClick={() => {
                setOverride({ ...r, effort: overrideRef?.effort ?? r.effort });
                modelM.close();
              }}
            >
              <ProviderMark provider={r.provider} model={r.model} size={16} />
              <span className="g">
                {modelLabel(models, r)}
                <span>
                  {i === 0 ? t("comp.modelPrimary") : t("comp.modelBackup")}
                  {r.effort ? ` · ${effortLabel(r.effort)}` : ""}
                  {ex ? ` · ${t("comp.modelExhausted")}${ex.until ? " · " + t("set.quotaResets", { time: until(ex.until) }) : ""}` : ""}
                </span>
              </span>
              <span className="ck">{overrideRef && sameRef(overrideRef, r) && <Check size={14} />}</span>
            </button>
          );
        })}
        {others.length > 0 && <div className="mlbl">{t("comp.modelAll")}</div>}
        {others.map((m) => (
          <button
            key={refString(m.ref)}
            className="mi"
            type="button"
            onClick={() => {
              setOverride({ ...m.ref, effort: overrideRef?.effort });
              modelM.close();
            }}
          >
            <ProviderMark provider={m.ref.provider} model={m.ref.model} size={16} />
            <span className="g">
              {m.info.name || m.info.id}
              <span>
                {m.provider.id}
                {m.info.contextWindow ? ` · ${kfmt(m.info.contextWindow)}` : ""}
              </span>
            </span>
            <span className="ck">{overrideRef && sameRef(overrideRef, m.ref) && <Check size={14} />}</span>
          </button>
        ))}
        <hr />
        <div className="mlbl">{t("comp.effort")}</div>
        <div className="seg">
          {(["", "low", "medium", "high"] as const).map((e) => (
            <button
              key={e}
              type="button"
              aria-pressed={(shown?.effort ?? "") === e}
              disabled={!shown}
              onClick={() => shown && setOverride({ ...shown, effort: e || undefined })}
            >
              {e ? effortLabel(e) : t("effort.default")}
            </button>
          ))}
        </div>
      </Popover>

      <Popover anchor={ctxM.anchor} open={ctxM.open} onClose={ctxM.close} placement="top" align="end" width={320}>
        <ContextPopover agent={agent} ctx={ctx} pct={pct} />
      </Popover>
    </div>
  );
}

function ContextPopover({ agent, ctx, pct }: { agent: AgentView; ctx?: { tokens: number; window: number; model?: ModelRef }; pct: number | null }) {
  const models = useStore((s) => s.models);
  return (
    <>
      <div className="ctxp">
        <div className="ch">
          <b>{t("ctx.title")}</b>
          <span>{pct === null ? "—" : t("ctx.used", { pct })}</span>
        </div>
        {ctx && pct !== null ? (
          <>
            <div className="cbar">
              <i style={{ flex: Math.max(pct, 1), background: pct >= 80 ? "var(--amber)" : "var(--accent)" }} />
              <i style={{ flex: Math.max(100 - pct, 0.001), background: "var(--active)" }} />
              <span className="thr" style={{ left: "80%" }} title="80%" />
            </div>
            <div className="small">
              {t("ctx.tokens", { used: kfmt(ctx.tokens), total: kfmt(ctx.window), model: ctx.model ? modelLabel(models, ctx.model) : "—" })}
            </div>
            <div className="clist">
              <div>
                <i style={{ background: pct >= 80 ? "var(--amber)" : "var(--accent)" }} />
                <span>{t("ctx.usedLabel")}</span>
                <b>{kfmt(ctx.tokens)}</b>
              </div>
              <div>
                <i style={{ background: "var(--active)" }} />
                <span>{t("ctx.free")}</span>
                <b>{kfmt(Math.max(0, ctx.window - ctx.tokens))}</b>
              </div>
            </div>
            <p className="small" style={{ margin: "8px 0 0" }}>
              {t("ctx.breakdownSoon")}
            </p>
          </>
        ) : (
          <p className="small" style={{ margin: 0 }}>
            {t("ctx.noData")}
          </p>
        )}
        <p className="small" style={{ margin: "8px 0 0" }}>
          {t("ctx.note")}
        </p>
      </div>
      <hr />
      <button className="mi" type="button" disabled title={t("common.soon")}>
        <Minimize2 size={16} />
        <span className="g">
          {t("ctx.compact")}
          <span>{t("common.soon")}</span>
        </span>
      </button>
      <button className="mi" type="button" disabled title={t("common.soon")}>
        <Plus size={16} />
        <span className="g">
          {t("ctx.newChat")}
          <span>{t("ctx.newChatDesc", { name: agent.name })} · {t("common.soon")}</span>
        </span>
      </button>
    </>
  );
}

export function shortPath(p: string): string {
  const m = p.match(/^\/(Users|home)\/[^/]+(\/.*)?$/);
  return m ? "~" + (m[2] ?? "") : p;
}
