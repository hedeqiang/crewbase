import { BookOpen, ClipboardList, FilePlus2, Megaphone, Trash2, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { API, type AgentView, type Working as WorkingData } from "../api";
import { ago } from "../format";
import { t, type Key } from "../i18n";
import { canManage, toast, useStore } from "../store";
import { Markdown } from "./Markdown";
import { confirmDialog } from "./Confirm";

/** An agent's working memory: tasks in progress, reference notes, and what
 *  it already told you. Reloaded when a turn ends (that is when it changes). */
export function useWorking(agent: AgentView) {
  const [w, setW] = useState<WorkingData | null>(null);
  const seq = useRef(0);
  // only the newest answer counts (switching agents, a turn ending)
  const load = () => {
    const n = ++seq.current;
    return API.working(agent.id).then(
      (r) => n === seq.current && setW(r),
      () => n === seq.current && setW(null),
    );
  };
  useEffect(() => {
    if (agent.kind === "chat") return;
    void load();
  }, [agent.id, agent.status]);
  return { w, reload: load };
}

const VIA = new Set(["routine", "goal", "followup", "job", "delegation", "approval", "workflow"]);

type Viewing = { kind: "task" | "note"; name: string; text: string } | null;

/** Task tab: the progress files of tasks the agent is working on. */
export function WorkTasks({ agent }: { agent: AgentView }) {
  const { w } = useWorking(agent);
  const [view, setView] = useState<Viewing>(null);
  if (!w || w.tasks.length === 0) return null;
  const open = async (name: string) => {
    try {
      const r = await API.taskProgress(agent.id, name);
      setView({ kind: "task", name, text: r.text });
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <>
      <div className="psec">{t("work.tasks", { n: w.tasks.length })}</div>
      {w.tasks.map((x) => (
        <button className="qi wrow" type="button" key={x.name} onClick={() => void open(x.name)}>
          <span className="n">
            <ClipboardList size={12} />
          </span>
          <span className="g">
            <span className="qt">{x.title || x.name}</span>
            <span className="small" style={{ display: "block" }}>
              {x.name} · {t("work.updated", { ago: ago(x.updated) })}
            </span>
          </span>
        </button>
      ))}
      {view && <Viewer agent={agent} view={view} onClose={() => setView(null)} />}
    </>
  );
}

/** Agent tab: reference notes (add, read, remove) and the told record. */
export function WorkNotes({ agent }: { agent: AgentView }) {
  const { w, reload } = useWorking(agent);
  const manage = useStore((s) => canManage(s));
  const [view, setView] = useState<Viewing>(null);
  const [toldOpen, setToldOpen] = useState(false);
  const file = useRef<HTMLInputElement>(null);
  if (agent.kind === "chat" || !w) return null;

  const add = async (files: FileList | null) => {
    for (const f of Array.from(files ?? [])) {
      if (f.size > 512 * 1024) {
        toast(t("work.tooBig", { name: f.name }));
        continue;
      }
      try {
        const r = await API.putNote(agent.id, f.name, await f.text());
        toast(t("work.added", { name: r.name }));
      } catch (e: any) {
        toast(e?.message ?? String(e));
      }
    }
    if (file.current) file.current.value = "";
    void reload();
  };
  const open = async (name: string) => {
    try {
      const r = await API.note(agent.id, name);
      setView({ kind: "note", name: r.name, text: r.text });
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  return (
    <>
      <div className="psec phead">
        <span>{t("work.notes", { n: w.notes.length })}</span>
        {manage && (
          <button className="btn sm ghost" type="button" onClick={() => file.current?.click()}>
            <FilePlus2 size={12} />
            {t("work.addNote")}
          </button>
        )}
        <input ref={file} type="file" accept=".md,.txt,text/markdown,text/plain" multiple hidden onChange={(e) => void add(e.target.files)} />
      </div>
      {w.notes.length === 0 ? (
        <p className="small">{t("work.notesEmpty")}</p>
      ) : (
        <div className="tree">
          {w.notes.map((n) => (
            <button key={n.name} className="tn" type="button" onClick={() => void open(n.name)} title={n.title}>
              <BookOpen size={13} />
              <span className="g">{n.name}</span>
              <span className="small">{kb(n.size)}</span>
            </button>
          ))}
        </div>
      )}
      {w.told.length > 0 && (
        <>
          <button className="psec phead toggle" type="button" aria-expanded={toldOpen} onClick={() => setToldOpen(!toldOpen)}>
            <span>{t("work.told", { n: w.told.length })}</span>
            <Megaphone size={12} />
          </button>
          {toldOpen && (
            <div className="told">
              {w.told
                .slice()
                .reverse()
                .slice(0, 12)
                .map((x, i) => (
                  <div key={i} className="toldi">
                    <span className="small">
                      {VIA.has(x.via) ? t(`work.via.${x.via}` as Key) : x.via} · {ago(x.time)}
                    </span>
                    <span>{x.text}</span>
                  </div>
                ))}
            </div>
          )}
        </>
      )}
      {view && (
        <Viewer
          agent={agent}
          view={view}
          onClose={() => setView(null)}
          onDelete={
            manage
              ? async () => {
                  if (!(await confirmDialog(t("work.deleteConfirm", { name: view.name })))) return;
                  try {
                    await API.deleteNote(agent.id, view.name);
                    setView(null);
                    void reload();
                  } catch (e: any) {
                    toast(e?.message ?? String(e));
                  }
                }
              : undefined
          }
        />
      )}
    </>
  );
}

/** Task lists read better as boxes than as "[x]". */
const checks = (s: string) => s.replace(/^(\s*[-*] )\[[xX]\] /gm, "$1☑ ").replace(/^(\s*[-*] )\[ \] /gm, "$1☐ ");

function Viewer({ agent, view, onClose, onDelete }: { agent: AgentView; view: NonNullable<Viewing>; onClose: () => void; onDelete?: () => void }) {
  useEffect(() => {
    const k = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal wide" role="dialog" aria-modal="true" aria-labelledby="wvTitle">
        <div className="mhd">
          <h3 id="wvTitle">
            {view.name}
            <span className="small muted"> · {view.kind === "task" ? t("work.taskOf", { name: agent.name }) : t("work.noteOf", { name: agent.name })}</span>
          </h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={15} />
          </button>
        </div>
        <div className="mbd wview">
          {view.name.endsWith(".txt") ? <pre className="codeblk">{view.text}</pre> : <Markdown text={checks(view.text)} />}
        </div>
        {onDelete && (
          <div className="mft">
            <button className="btn dng" type="button" onClick={onDelete}>
              <Trash2 size={13} />
              {t("work.deleteNote")}
            </button>
          </div>
        )}
      </div>
    </>,
    document.body,
  );
}

function kb(n: number) {
  return n < 1024 ? `${n} B` : `${Math.round(n / 1024)} KB`;
}
