import { Archive, Bot, Check, FolderInput, FolderMinus, Hash, MessageSquare, MoreHorizontal, Pencil, Trash2 } from "lucide-react";
import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { API, type AgentView, type SessionMeta } from "../api";
import { t } from "../i18n";
import { getState, isChat, newChat, openNewAgent, openNewChannel, resync, setState, toast, useFolders, useStore } from "../store";
import { teamOf } from "../api";
import { Avatar } from "./Avatar";

type Target = { kind: "agent"; agent: AgentView } | { kind: "channel"; meta: SessionMeta } | { kind: "folder"; name: string };

interface Open {
  x: number;
  y: number;
  target: Target;
}

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
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
  const fromButton = e.type === "click";
  opener?.({ x: fromButton ? r.right - 4 : e.clientX, y: fromButton ? r.bottom + 4 : e.clientY, target });
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
  const ref = useRef<HTMLDivElement>(null);
  const folders = useFolders();

  useEffect(() => {
    opener = setOpen;
    creator = () => setDialog({ kind: "create", target: { kind: "folder", name: "" } });
    adder = (folder) => setDialog({ kind: "add", target: { kind: "folder", name: folder } });
    return () => {
      opener = null;
      creator = null;
      adder = null;
    };
  }, []);
  useEffect(() => {
    if (!open) return;
    const close = (e: Event) => {
      if (ref.current && e.target instanceof Node && ref.current.contains(e.target)) return;
      setOpen(null);
    };
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        setOpen(null);
      }
    };
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", esc);
    window.addEventListener("blur", close);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", esc);
      window.removeEventListener("blur", close);
    };
  }, [open]);

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
    const left = Math.min(open.x, window.innerWidth - 220);
    const top = Math.min(open.y, window.innerHeight - 200);
    return (
      <div ref={ref} className="pop menu rowmenu" style={{ left, top, width: 210 }} role="menu">
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
      </div>
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
      .map((a) => ({ id: "a:" + a.id, name: a.kind === "chat" ? summaries[a.session]?.topic || a.name : a.name, sub: a.folder || t("rm.topLevel"), agent: a })),
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
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal rmd" role="dialog" aria-modal="true" aria-label={t("rm.addTitle", { folder })}>
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
      </div>
    </>
  );
}

function RowDialog({ d, folders, onClose }: { d: { kind: DialogKind; target: Target }; folders: string[]; onClose: () => void }) {
  const tg = d.target;
  const current = tg.kind === "agent" ? tg.agent.folder ?? "" : tg.kind === "channel" ? tg.meta.folder ?? "" : tg.name;
  const [val, setVal] = useState(d.kind === "move" ? current : d.kind === "create" ? "" : nameOf(tg));
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    input.current?.select();
  }, []);

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
        if (tg.kind === "agent") await API.patchAgent(tg.agent.id, { folder: v });
        else if (tg.kind === "channel") await API.updateChannel(tg.meta.id, { folder: v });
        return v ? t("rm.moved", { folder: v }) : t("rm.movedTop");
      }
      // delete
      if (tg.kind === "agent") {
        await API.deleteAgent(tg.agent.id);
        if (getState().current === tg.agent.session) setState({ current: null });
        return isChat(tg.agent) ? t("rm.deletedChat") : t("rm.deletedAgent", { name: tg.agent.name });
      }
      if (tg.kind === "channel") {
        await API.deleteChannel(tg.meta.id);
        if (getState().current === tg.meta.id) setState({ current: null });
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
    <>
      <div className="ovscrim show" onMouseDown={onClose} />
      <div className="modal sm" role="dialog" aria-modal="true" aria-label={title}>
        <div className="mhd">
          <h3>{title}</h3>
        </div>
        <div className="mbd">
        {note && <p className="small rmnote">{note}</p>}
        {d.kind !== "delete" && (
          <>
            <input
              ref={input}
              className="fi"
              value={val}
              list={d.kind === "move" ? "rmFolders" : undefined}
              placeholder={d.kind === "move" ? t("rm.folderPh") : d.kind === "create" ? t("rm.newFolderPh") : ""}
              onChange={(e) => setVal(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void submit();
                if (e.key === "Escape") {
                  e.preventDefault();
                  onClose();
                }
              }}
            />
            {d.kind === "move" && (
              <datalist id="rmFolders">
                {folders.map((f) => (
                  <option key={f} value={f} />
                ))}
              </datalist>
            )}
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
      </div>
    </>
  );
}
