import { Archive, ArrowUpToLine, Repeat, Bot, Check, Folder, FolderInput, FolderMinus, FolderPlus, Hash, MessageSquare, MoreHorizontal, Pencil, Trash2 } from "lucide-react";
import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { API, type AgentView, type SessionMeta } from "../api";
import { t } from "../i18n";
import { chatTitle, isChat, newChat, openNewAgent, openNewChannel, openSession, refreshSessions, resync, setState, toast, useFolders, useStore } from "../store";
import { requestAutoDraft } from "./conversation/autoDraft";
import { teamOf } from "../api";
import { Avatar } from "./Avatar";
import { Popover } from "./Popover";

type Target = { kind: "agent"; agent: AgentView } | { kind: "channel"; meta: SessionMeta } | { kind: "folder"; name: string };

interface Open {
  /** the row it is for: the menu (and its small forms) open below it */
  anchor: HTMLElement | null;
  target: Target;
}
// the row of the last menu: a form opened from it goes there too
let lastAnchor: HTMLElement | null = null;
const plusButton = () => document.querySelector<HTMLElement>(".sbar button[aria-haspopup], button[aria-haspopup][aria-label]");

let opener: ((o: Open) => void) | null = null;
let creator: (() => void) | null = null;

/** Opens the 新建文件夹 dialog (list ＋ menu). */
export function openNewFolder() {
  creator?.();
}

/** Opens the row menu for a target at the pointer (right click) or below an
 *  element (the ⋯ button, keyboard). */
export function openRowMenu(e: MouseEvent, target: Target) {
  e.preventDefault();
  e.stopPropagation();
  const el = e.currentTarget as HTMLElement;
  const anchor = el.closest<HTMLElement>(".ci, [data-folder], .fold, .fh") ?? el;
  lastAnchor = anchor;
  opener?.({ anchor, target });
}

/** The ⋯ button shown on hover at the end of a list row. */
export function RowMenuButton({ target }: { target: Target }) {
  return (
    <span
      role="button"
      tabIndex={0}
      className="rowmore"
      aria-label={t("rm.more")}
      title={t("rm.more")}
      onClick={(e) => openRowMenu(e, target)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") openRowMenu(e as unknown as MouseEvent, target);
      }}
    >
      <MoreHorizontal size={15} />
    </span>
  );
}

/** Mounted once (in the session list): the menu and its small dialogs. */
export function RowMenuHost() {
  const [open, setOpen] = useState<Open | null>(null);
  const [dialog, setDialog] = useState<{ kind: DialogKind; target: Target } | null>(null);
  const folders = useFolders();

  useEffect(() => {
    opener = setOpen;
    creator = () => {
      lastAnchor = plusButton();
      setDialog({ kind: "create", target: { kind: "folder", name: "" } });
    };
    adder = (folder) => setDialog({ kind: "add", target: { kind: "folder", name: folder } });
    return () => {
      opener = null;
      creator = null;
      adder = null;
    };
  }, []);

  const item = (icon: ReactNode, label: string, run: () => void, danger = false) => (
    <button
      type="button"
      className={"mi" + (danger ? " dng" : "")}
      onClick={() => {
        setOpen(null);
        run();
      }}
    >
      {icon}
      <span className="g">{label}</span>
    </button>
  );

  const menu = () => {
    if (!open) return null;
    const tg = open.target;
    return (
      <Popover anchor={open.anchor} open onClose={() => setOpen(null)} width={240} className="rowmenu">
        {tg.kind === "folder" ? (
          <>
            {item(<MessageSquare size={15} />, t("rm.folderNewChat"), () => void newChat(tg.name))}
            {item(<Bot size={15} />, t("rm.folderNewAgent"), () => openNewAgent(tg.name))}
            {item(<Hash size={15} />, t("rm.folderNewChannel"), () => openNewChannel(tg.name))}
            {item(<FolderInput size={15} />, t("rm.folderAddExisting"), () => setDialog({ kind: "add", target: tg }))}
            <div className="msep" />
            {item(<Pencil size={15} />, t("rm.renameFolder"), () => setDialog({ kind: "rename", target: tg }))}
            {item(<FolderMinus size={15} />, t("rm.dissolve"), () => setDialog({ kind: "delete", target: tg }), true)}
          </>
        ) : (
          <>
            {item(<Pencil size={15} />, t("rm.rename"), () => setDialog({ kind: "rename", target: tg }))}
            {item(<FolderInput size={15} />, t("rm.move"), () => setDialog({ kind: "move", target: tg }))}
            {item(<Repeat size={15} />, t("c2r.menu"), () => {
              const sid = tg.kind === "agent" ? tg.agent.session : tg.meta.id;
              if (!sid) return;
              openSession(sid);
              requestAutoDraft(sid);
            })}
            {tg.kind === "channel" &&
              item(<Archive size={15} />, t("rm.archive"), () =>
                act(async () => {
                  await API.channelAction(tg.meta.id, "archive");
                  return t("rm.archived", { name: tg.meta.title || tg.meta.id });
                }),
              )}
            <div className="msep" />
            {item(<Trash2 size={15} />, tg.kind === "agent" ? (isChat(tg.agent) ? t("rm.deleteChat") : t("rm.deleteAgent")) : t("rm.deleteChannel"), () => setDialog({ kind: "delete", target: tg }), true)}
          </>
        )}
      </Popover>
    );
  };

  return (
    <>
      {menu()}
      {dialog && dialog.kind === "add" && dialog.target.kind === "folder" && <AddToFolder folder={dialog.target.name} onClose={() => setDialog(null)} />}
      {dialog && dialog.kind !== "add" && <RowDialog d={dialog} folders={folders} onClose={() => setDialog(null)} />}
    </>
  );
}

async function act(fn: () => Promise<string>) {
  try {
    toast(await fn());
    await resync();
  } catch (e: any) {
    toast(t("rm.failed", { error: e?.message ?? String(e) }));
  }
}

function nameOf(tg: Target) {
  return tg.kind === "agent" ? tg.agent.name : tg.kind === "channel" ? tg.meta.title || tg.meta.id : tg.name;
}

type DialogKind = "rename" | "move" | "delete" | "create" | "add";

/** Opens a folder's 「添加已有…」 picker. */
export function openAddToFolder(folder: string) {
  adder?.(folder);
}
let adder: ((folder: string) => void) | null = null;

/** 把已有的 Agent / 频道 / 对话放进文件夹: tick several, move them at once. */
function AddToFolder({ folder, onClose }: { folder: string; onClose: () => void }) {
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const team = useStore((s) => s.team);
  const summaries = useStore((s) => s.summaries);
  const [pick, setPick] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const rows = [
    ...Object.values(agents)
      .filter((a) => teamOf(a) === team && (a.folder ?? "") !== folder)
      .map((a) => ({ id: "a:" + a.id, name: a.kind === "chat" ? chatTitle(a, summaries[a.session]?.topic) : a.name, sub: a.folder || t("rm.topLevel"), agent: a })),
    ...Object.values(sessions)
      .filter((m) => m.kind === "channel" && teamOf(m) === team && (m.folder ?? "") !== folder)
      .map((m) => ({ id: "c:" + m.id, name: "# " + (m.title || m.id), sub: m.folder || t("rm.topLevel"), agent: undefined })),
  ];
  const toggle = (id: string) =>
    setPick((p) => {
      const n = new Set(p);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const save = async () => {
    setBusy(true);
    try {
      for (const id of pick) {
        if (id.startsWith("a:")) await API.patchAgent(id.slice(2), { folder });
        else await API.updateChannel(id.slice(2), { folder });
      }
      toast(t("rm.addedToFolder", { n: pick.size, folder }));
      await resync();
      onClose();
    } catch (e: any) {
      toast(t("rm.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Popover anchor={lastAnchor} open onClose={onClose} width={320} className="rmform rmd">
        <div className="mhd">
          <b>{t("rm.addTitle", { folder })}</b>
        </div>
        <div className="mbd">
          {rows.length === 0 ? (
            <p className="small">{t("rm.addNone")}</p>
          ) : (
            <div className="mpick" role="listbox" aria-multiselectable="true">
              {rows.map((r) => (
                <button key={r.id} type="button" role="option" aria-selected={pick.has(r.id)} className="mopt" onClick={() => toggle(r.id)}>
                  {r.agent ? <Avatar agent={r.agent} size={22} /> : <Hash size={16} className="muted" />}
                  <span className="g">
                    <b>{r.name}</b>
                    <span className="small">{r.sub}</span>
                  </span>
                  <span className="ck">{pick.has(r.id) && <Check size={14} />}</span>
                </button>
              ))}
            </div>
          )}
        </div>
        <div className="mft">
          <button className="btn ghost" type="button" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button className="btn pri" type="button" disabled={busy || pick.size === 0} onClick={() => void save()}>
            {t("rm.addBtn", { n: pick.size })}
          </button>
        </div>
      </Popover>
  );
}

function RowDialog({ d, folders, onClose }: { d: { kind: DialogKind; target: Target }; folders: string[]; onClose: () => void }) {
  const tg = d.target;
  const current = tg.kind === "agent" ? tg.agent.folder ?? "" : tg.kind === "channel" ? tg.meta.folder ?? "" : tg.name;
  const [val, setVal] = useState(d.kind === "move" ? current : d.kind === "create" ? "" : nameOf(tg));
  const [busy, setBusy] = useState(false);
  // 移到: a folder from the list (or a new one), never typed blind
  const NEW = "\u0000new";
  const [dest, setDest] = useState<string>(current);
  const input = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (d.kind === "move") listRef.current?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus();
    else input.current?.select();
  }, []);
  useEffect(() => {
    if (dest === NEW) setTimeout(() => input.current?.focus(), 0);
  }, [dest]);

  const submit = async () => {
    setBusy(true);
    await act(async () => {
      const v = val.trim();
      if (d.kind === "create") {
        if (!v) throw new Error(t("rm.needName"));
        if (folders.includes(v)) throw new Error(t("rm.folderExists", { name: v }));
        await API.createFolder(v);
        return t("rm.folderCreated", { name: v });
      }
      if (d.kind === "rename") {
        if (!v) throw new Error(t("rm.needName"));
        if (tg.kind === "agent") await API.patchAgent(tg.agent.id, { name: v });
        else if (tg.kind === "channel") await API.updateChannel(tg.meta.id, { title: v });
        else await API.renameFolder(tg.name, v);
        return t("rm.renamed", { name: v });
      }
      if (d.kind === "move") {
        const v = dest === NEW ? val.trim() : dest;
        if (dest === NEW && !v) throw new Error(t("rm.needName"));
        if (tg.kind === "agent") await API.patchAgent(tg.agent.id, { folder: v });
        else if (tg.kind === "channel") await API.updateChannel(tg.meta.id, { folder: v });
        return v ? t("rm.moved", { folder: v }) : t("rm.movedTop");
      }
      // delete
      if (tg.kind === "agent") {
        await API.deleteAgent(tg.agent.id);
        const gone = tg.agent;
        setState((s) => {
          const { [gone.id]: _a, ...agents } = s.agents;
          const hist = s.hist.filter((x) => x !== gone.session);
          return { agents, hist, histPos: Math.min(s.histPos, hist.length - 1), current: s.current === gone.session ? null : s.current };
        });
        void refreshSessions();
        return isChat(tg.agent) ? t("rm.deletedChat") : t("rm.deletedAgent", { name: tg.agent.name });
      }
      if (tg.kind === "channel") {
        await API.deleteChannel(tg.meta.id);
        // gone from the list now, not only when the daemon's event arrives
        const id = tg.meta.id;
        setState((s) => {
          const { [id]: _gone, ...sessions } = s.sessions;
          const hist = s.hist.filter((x) => x !== id);
          return { sessions, hist, histPos: Math.min(s.histPos, hist.length - 1), current: s.current === id ? null : s.current };
        });
        void refreshSessions();
        return t("rm.deletedChannel", { name: nameOf(tg) });
      }
      await API.dissolveFolder(tg.name);
      return t("rm.dissolved", { name: tg.name });
    });
    setBusy(false);
    onClose();
  };

  const title =
    d.kind === "create"
      ? t("list.newFolder")
      : d.kind === "rename"
      ? tg.kind === "folder"
        ? t("rm.renameFolder")
        : t("rm.rename")
      : d.kind === "move"
        ? t("rm.moveTitle", { name: nameOf(tg) })
        : tg.kind === "agent"
          ? isChat(tg.agent)
            ? t("rm.deleteChatQ")
            : t("rm.deleteAgentQ", { name: nameOf(tg) })
          : tg.kind === "channel"
            ? t("rm.deleteChannelQ", { name: nameOf(tg) })
            : t("rm.dissolveQ", { name: nameOf(tg) });

  const note =
    d.kind !== "delete"
      ? ""
      : tg.kind === "agent"
        ? isChat(tg.agent)
          ? t("rm.deleteChatNote")
          : t("rm.deleteAgentNote")
        : tg.kind === "channel"
          ? t("rm.deleteChannelNote")
          : t("rm.dissolveNote");

  return (
    <Popover anchor={lastAnchor} open onClose={onClose} width={320} className="rmform sm">
        <div className="mhd">
          <h3>{title}</h3>
        </div>
        <div className="mbd">
        {note && <p className="small rmnote">{note}</p>}
        {d.kind === "move" && (
          <div
            className="fpick"
            role="radiogroup"
            aria-label={title}
            ref={listRef}
            onKeyDown={(e) => {
              const opts = ["", ...folders, NEW];
              const i = opts.indexOf(dest);
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                const n = opts[(i + (e.key === "ArrowDown" ? 1 : opts.length - 1)) % opts.length];
                setDest(n);
                setTimeout(() => listRef.current?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus(), 0);
              } else if (e.key === "Enter" && dest !== NEW) {
                e.preventDefault();
                void submit();
              }
            }}
          >
            {["", ...folders].map((f) => (
              <button key={f || "_top"} type="button" role="radio" aria-checked={dest === f} tabIndex={dest === f ? 0 : -1} className="fpo" onClick={() => setDest(f)} onDoubleClick={() => void submit()}>
                {f ? <Folder size={14} /> : <ArrowUpToLine size={14} />}
                <span className="g">{f || t("rm.top")}</span>
                {f === current && <span className="small muted">{t("rm.here")}</span>}
                {dest === f && <Check size={14} className="ck" />}
              </button>
            ))}
            <button type="button" role="radio" aria-checked={dest === NEW} tabIndex={dest === NEW ? 0 : -1} className="fpo" onClick={() => setDest(NEW)}>
              <FolderPlus size={14} />
              <span className="g">{t("list.newFolder")}…</span>
              {dest === NEW && <Check size={14} className="ck" />}
            </button>
            {dest === NEW && (
              <input
                ref={input}
                className="fi"
                value={val === current ? "" : val}
                placeholder={t("rm.newFolderPh")}
                onChange={(e) => setVal(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") void submit();
                }}
              />
            )}
          </div>
        )}
        {d.kind !== "delete" && d.kind !== "move" && (
          <>
            <input
              ref={input}
              className="fi"
              value={val}
              placeholder={d.kind === "create" ? t("rm.newFolderPh") : ""}
              onChange={(e) => setVal(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void submit();
                if (e.key === "Escape") {
                  e.preventDefault();
                  onClose();
                }
              }}
            />
          </>
        )}
        <div className="mft">
          <button className="btn" type="button" onClick={onClose}>
            {t("common.cancel")}
          </button>
          <button className={"btn " + (d.kind === "delete" ? "dngfill" : "pri")} type="button" disabled={busy} onClick={() => void submit()}>
            {d.kind === "delete" ? (tg.kind === "folder" ? t("rm.dissolveBtn") : t("rm.deleteBtn")) : d.kind === "create" ? t("rm.create") : t("common.save")}
          </button>
        </div>
        </div>
      </Popover>
  );
}
