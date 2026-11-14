// 任务板 (A-7): the task DAG shared by a conversation and its sub-agents.
// Shown in the right panel's 任务 tab; agents edit it with task_* tools,
// people here. Live via SSE "board.changed".

import { Check, CircleDashed, CircleDot, Loader2, OctagonAlert, Plus, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { API, type BoardView, type TaskView } from "../api";
import { t, type Key } from "../i18n";
import { toast, useStore } from "../store";
import { Avatar } from "./Avatar";

const ICON: Record<string, React.ReactNode> = {
  todo: <CircleDashed size={14} />,
  doing: <CircleDot size={14} />,
  blocked: <OctagonAlert size={14} />,
  done: <Check size={14} />,
};

export function TaskBoard({ session }: { session: string }) {
  const [board, setBoard] = useState<BoardView | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const agents = useStore((s) => s.agents);
  const tier = useStore((s) => s.tier);
  const tick = useStore((s) => (board ? s.boardTick[board.root] ?? 0 : 0));
  const edit = tier !== "viewer";

  useEffect(() => {
    let live = true;
    API.board(session).then(
      (b) => live && setBoard(b),
      () => live && setBoard(null),
    );
    return () => {
      live = false;
    };
  }, [session, tick]);

  const owners = useMemo(() => Object.values(agents).filter((a) => a.kind !== "chat"), [agents]);
  const byId = useMemo(() => Object.fromEntries((board?.tasks ?? []).map((x) => [x.id, x])), [board]);

  if (!board) return null;
  const done = board.tasks.filter((x) => x.status === "done").length;

  const change = async (task: TaskView, b: Record<string, unknown>) => {
    try {
      const v = await API.updateTask(session, task.id, b);
      setBoard((bd) => (bd ? { ...bd, tasks: bd.tasks.map((x) => (x.id === v.id ? v : x)) } : bd));
      // readiness of others may change
      setBoard(await API.board(session));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  const add = async () => {
    if (!title.trim()) return;
    setBusy(true);
    try {
      await API.addTask(session, { title: title.trim() });
      setTitle("");
      setAdding(false);
      setBoard(await API.board(session));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (task: TaskView) => {
    try {
      await API.removeTask(session, task.id);
      setOpen(null);
      setBoard(await API.board(session));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  const ownerLabel = (o?: string) => {
    if (!o) return t("board.unassigned");
    if (o.startsWith("member:")) return o.slice(7) === "owner" ? t("board.you") : o.slice(7);
    return agents[o]?.name ?? o;
  };

  return (
    <>
      <div className="psec boardhead">
        <span>{board.tasks.length ? t("board.title", { done, n: board.tasks.length }) : t("board.titleEmpty")}</span>
        {edit && (
          <button className="ix" type="button" aria-label={t("board.add")} title={t("board.add")} onClick={() => setAdding((v) => !v)}>
            <Plus size={13} />
          </button>
        )}
      </div>
      {board.tasks.length > 0 && (
        <div className="bprog" aria-hidden>
          <i style={{ width: `${(done / board.tasks.length) * 100}%` }} />
        </div>
      )}
      {adding && (
        <form
          className="badd"
          onSubmit={(e) => {
            e.preventDefault();
            void add();
          }}
        >
          <input className="fi sm" autoFocus value={title} maxLength={120} placeholder={t("board.addPh")} onChange={(e) => setTitle(e.target.value)} />
          <button className="btn pri sm" type="submit" disabled={busy || !title.trim()}>
            {busy ? <Loader2 size={12} className="spin" /> : t("common.add")}
          </button>
        </form>
      )}
      {board.tasks.length === 0 && !adding && <p className="small boardempty">{t("board.empty")}</p>}
      <ul className="board">
        {board.tasks.map((x) => {
          const ag = x.owner ? agents[x.owner] : undefined;
          return (
            <li key={x.id} className={"btask s-" + x.status + (x.ready ? " ready" : "")}>
              <button type="button" className="bt" aria-expanded={open === x.id} onClick={() => setOpen(open === x.id ? null : x.id)}>
                <span className="bi" title={t(`board.st.${x.ready ? "ready" : x.status}` as Key)}>
                  {ICON[x.status]}
                </span>
                <span className="bg">
                  <b>
                    <span className="bid">#{x.id}</span> {x.title}
                  </b>
                  <span>
                    {ag ? <Avatar agent={ag} size={14} /> : null} {ownerLabel(x.owner)}
                    {x.waiting?.length ? ` · ${t("board.waits", { ids: x.waiting.map((d) => "#" + d).join(" ") })}` : ""}
                    {x.ready ? ` · ${t("board.st.ready")}` : ""}
                  </span>
                </span>
              </button>
              {open === x.id && (
                <div className="bdet">
                  {x.detail && <p className="small">{x.detail}</p>}
                  {x.note && <p className={"small bnote" + (x.status === "blocked" ? " blk" : "")}>{x.note}</p>}
                  {x.deps?.length ? (
                    <p className="small">
                      {t("board.after")}{" "}
                      {x.deps.map((d) => (
                        <span key={d} className={"bdep" + (byId[d]?.status === "done" ? " ok" : "")}>
                          #{d} {byId[d]?.title ?? ""}
                        </span>
                      ))}
                    </p>
                  ) : null}
                  {x.scope?.length ? (
                    <p className="small" title={t("board.scopeHint")}>
                      {t("board.scope")} <span className="mono">{x.scope.join(", ")}</span>
                    </p>
                  ) : null}
                  <p className="small bmeta">{t("board.by", { by: x.by.replace(/^person:/, "") })}</p>
                  {edit && (
                    <div className="bacts">
                      <span className="ssel sm">
                        <select value={x.status} aria-label={t("board.status")} onChange={(e) => void change(x, { status: e.target.value, ...(e.target.value === "blocked" && !x.note ? { note: window.prompt(t("board.blockedWhy")) ?? "" } : {}) })}>
                          {["todo", "doing", "blocked", "done"].map((s) => (
                            <option key={s} value={s}>
                              {t(`board.st.${s}` as Key)}
                            </option>
                          ))}
                        </select>
                      </span>
                      <span className="ssel sm">
                        <select value={x.owner ?? ""} aria-label={t("board.owner")} onChange={(e) => void change(x, { owner: e.target.value })}>
                          <option value="">{t("board.unassigned")}</option>
                          <option value="member:owner">{t("board.you")}</option>
                          {owners.map((a) => (
                            <option key={a.id} value={a.id}>
                              {a.name}
                            </option>
                          ))}
                        </select>
                      </span>
                      <button className="ix dng" type="button" title={t("board.remove")} aria-label={t("board.remove")} onClick={() => void remove(x)}>
                        <Trash2 size={12} />
                      </button>
                      <button className="ix" type="button" aria-label={t("common.close")} onClick={() => setOpen(null)}>
                        <X size={12} />
                      </button>
                    </div>
                  )}
                </div>
              )}
            </li>
          );
        })}
      </ul>
    </>
  );
}
