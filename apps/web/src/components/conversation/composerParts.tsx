import { useEffect, useRef, useState } from "react";
import type React from "react";
import { Check, ChevronDown, ChevronRight, ChevronLeft, CornerLeftUp, Folder, FolderOpen, Clock, Undo2, Shield, ShieldAlert, Eye, ListPlus, ChevronUp, CornerDownRight, X } from "lucide-react";
import { API, desktop, type AgentView } from "../../api";
import { t } from "../../i18n";
import { refreshAgent, setState, toast } from "../../store";
import { shortPath, dirName, recentDirs, rememberDir, tailPath } from "./util";

/** Folders on the host, one level at a time (no native picker here). */
function DirBrowser({ start, busy, onPick, onBack }: { start: string; busy: boolean; onPick: (dir: string) => void; onBack: () => void }) {
  const [at, setAt] = useState<{ path: string; parent: string; home: string; dirs: { name: string; path: string }[] } | null>(null);
  const [err, setErr] = useState("");
  const seq = useRef(0);
  // fast clicks: only the newest folder counts
  const go = (p?: string) => {
    const n = ++seq.current;
    return API.browseDirs(p).then(
      (r) => {
        if (n !== seq.current) return;
        setAt(r);
        setErr("");
      },
      (e: any) => {
        if (n !== seq.current) return;
        if (e?.status === 404 && p) return void go(); // the folder is gone: start at home
        setErr(e?.status === 403 ? t("dir.browseOwner") : e?.message ?? String(e));
      },
    );
  };
  useEffect(() => {
    void go(start || undefined);
  }, []);
  const atHome = at && at.path === at.home;
  return (
    <div className="dirbrowse">
      <div className="dbhead">
        <button className="ix" type="button" aria-label={t("ap.back")} onClick={onBack}>
          <ChevronLeft size={15} />
        </button>
        <span className="mono g" title={at?.path}>
          {at ? tailPath(shortPath(at.path)) : "…"}
        </span>
        {at && !atHome && (
          <button className="btn sm ghost" type="button" disabled={busy} onClick={() => onPick(at.path)}>
            {t("dir.useHere")}
          </button>
        )}
      </div>
      {err && <p className="small errt" style={{ margin: "6px 9px" }}>{err}</p>}
      <div className="dblist" key={at?.path}>
        {at && !atHome && at.parent && (
          <button className="mi" type="button" onClick={() => void go(at.parent)}>
            <CornerLeftUp size={15} />
            <span className="g">{t("dir.up")}</span>
          </button>
        )}
        {at?.dirs.map((x) => (
          <div key={x.path} className="dbrow">
            <button className="mi" type="button" title={t("dir.open")} onClick={() => void go(x.path)}>
              <Folder size={15} />
              <span className="g">{x.name}</span>
              <ChevronRight size={13} className="muted" />
            </button>
            <button className="btn sm dbuse" type="button" disabled={busy} onClick={() => onPick(x.path)}>
              {t("dir.useShort")}
            </button>
          </div>
        ))}
        {at && at.dirs.length === 0 && <p className="small muted" style={{ margin: "6px 9px" }}>{t("dir.noSub")}</p>}
      </div>
    </div>
  );
}

/** The composer's folder menu: where this agent works (a project folder
 *  or its own workspace). */
export function DirMenu({ agent, onDone }: { agent: AgentView; onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const d = desktop();
  const project = agent.sharedWorkspace || "";
  const use = async (dir: string) => {
    setBusy(true);
    try {
      const a = await API.setProjectDir(agent.id, dir);
      setState((s) => ({ agents: { ...s.agents, [a.id]: a } }));
      if (dir) rememberDir(a.workspace);
      toast(dir ? t("dir.set", { dir: shortPath(a.workspace) }) : t("dir.reset"));
      onDone();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  // the system's folder picker in the desktop app; elsewhere (a browser,
  // the relay) a folder browser served by the host
  const pick = async () => {
    if (!d?.pickFolder) return setBrowsing(true);
    const dir = await d.pickFolder(project || undefined).catch(() => "");
    if (dir) await use(dir);
  };
  if (browsing) return <DirBrowser start={project || ""} busy={busy} onPick={(dir) => void use(dir)} onBack={() => setBrowsing(false)} />;
  const recents = recentDirs().filter((x) => x !== project);
  return (
    <>
      <div className="mlbl">{t("dir.title", { name: agent.name })}</div>
      <button
        className="mi"
        type="button"
        title={t("comp.copyPath")}
        onClick={() => void navigator.clipboard?.writeText(agent.workspace).then(() => toast(t("comp.copied")), () => {})}
      >
        <Folder size={16} />
        <span className="g">
          {project ? dirName(project, true) : t("dir.default")}
          <span className="mono">{shortPath(agent.workspace)}</span>
        </span>
        <span className="ck">
          <Check size={14} />
        </span>
      </button>
      {recents.map((r) => (
        <button key={r} className="mi" type="button" disabled={busy} onClick={() => void use(r)}>
          <Clock size={16} />
          <span className="g">
            {dirName(r, true)}
            <span className="mono">{shortPath(r)}</span>
          </span>
        </button>
      ))}
      <hr />
      <button className="mi" type="button" disabled={busy} onClick={() => void pick()}>
        <FolderOpen size={16} />
        <span className="g">{t("dir.pick")}</span>
      </button>
      {project && (
        <button className="mi" type="button" disabled={busy} onClick={() => void use("")}>
          <Undo2 size={16} />
          <span className="g">
            {t("dir.back")}
            <span>{t("dir.backDesc")}</span>
          </span>
        </button>
      )}
      <p className="small" style={{ margin: "4px 9px 6px" }}>
        {t("dir.note")}
      </p>
    </>
  );
}


/** A run mode's icon: look (只读), shield (沙箱), shield with a mark (完全访问). */
export function ModeIcon({ mode, size }: { mode: string; size: number }) {
  return mode === "read-only" ? <Eye size={size} /> : mode === "host" ? <ShieldAlert size={size} /> : <Shield size={size} />;
}

/** The messages waiting for the running turn (排队), above the composer:
 *  sooner / later, 插话 (join the turn now) and cancel. */
export function QueueStack({ agent, session, busy }: { agent: AgentView; session: string; busy: boolean }) {
  const items = agent.queue.filter((q) => q.session === session && !q.from);
  if (!items.length) return null;
  // an input the daemon no longer has queued (claimed, or gone) answers
  // 404: the queue shown was out of date, so read it again, no error
  const act = (p: Promise<unknown>) =>
    void p.catch((e) => {
      if (e?.status === 404) void refreshAgent(agent.id);
      else toast(e?.message ?? String(e));
    });
  const all = agent.queue;
  return (
    <div className="qstack" aria-label={t("chat.queue", { n: items.length })}>
      <div className="qshd small">
        <ListPlus size={13} />
        {t("qs.head", { n: items.length })}
      </div>
      {items.map((q) => {
        const at = all.findIndex((x) => x.id === q.id);
        const joining = !q.queue && busy;
        return (
          <div className="qsrow" key={q.id}>
            <span className="qstx">{q.text || t("qs.attach")}</span>
            {joining ? (
              <span className="small muted qsj">{t("qs.joining")}</span>
            ) : (
              <>
                <button className="ix" type="button" disabled={at <= 0} aria-label={t("qs.up")} title={t("qs.up")} onClick={() => act(API.moveInput(agent.id, q.id, -1))}>
                  <ChevronUp size={14} />
                </button>
                <button className="ix" type="button" disabled={at >= all.length - 1} aria-label={t("qs.down")} title={t("qs.down")} onClick={() => act(API.moveInput(agent.id, q.id, 1))}>
                  <ChevronDown size={14} />
                </button>
                {busy && (
                  <button
                    className="btn sm qssteer"
                    type="button"
                    title={t("steer.nowTitle")}
                    onClick={() => act(API.steer(agent.id, q.id).then(() => toast(t("steer.sent", { name: agent.name }))))}
                  >
                    <CornerDownRight size={13} />
                    {t("steer.now")}
                  </button>
                )}
              </>
            )}
            <button className="ix" type="button" aria-label={t("panel.queueCancel")} title={t("panel.queueCancel")} onClick={() => act(API.cancelInput(agent.id, q.id))}>
              <X size={14} />
            </button>
          </div>
        );
      })}
    </div>
  );
}

/** One of the composer's choices (模型, 运行环境, 上下文), opened in place
 *  above the text box rather than floating over the conversation. Esc or
 *  ✕ closes it, and the keyboard goes back to the text box. */
export function Tray({ title, onClose, className = "", children }: { title: string; onClose: () => void; className?: string; children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const closeBack = () => {
    // found before it closes (then the tray is gone)
    const box = ref.current?.closest(".composer")?.querySelector<HTMLElement>("textarea");
    onClose();
    requestAnimationFrame(() => box?.focus({ preventScroll: true }));
  };
  // the first field (a search), else its first choice, takes the keyboard
  useEffect(() => {
    const id = window.setTimeout(() => {
      const el = ref.current;
      (el?.querySelector<HTMLElement>(".ctrayb input") ?? el?.querySelector<HTMLElement>(".ctrayb button:not(:disabled)"))?.focus({ preventScroll: true });
    }, 0);
    return () => window.clearTimeout(id);
  }, []);
  return (
    <div
      ref={ref}
      className={"ctray " + className}
      role="region"
      aria-label={title}
      onKeyDown={(e) => {
        if (e.key === "Escape" && !e.defaultPrevented) {
          e.preventDefault();
          e.stopPropagation();
          closeBack();
        }
      }}
    >
      <div className="ctrayh">
        <b>{title}</b>
        <button className="ix" type="button" aria-label={t("common.close")} onClick={closeBack}>
          <X size={14} />
        </button>
      </div>
      <div className="ctrayb">{children}</div>
    </div>
  );
}
