import { Archive, FolderInput, FolderMinus, MoreHorizontal, Pencil, Trash2 } from "lucide-react";
import { useEffect, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { API, type AgentView, type SessionMeta } from "../api";
import { t } from "../i18n";
import { getState, resync, setState, toast, useStore } from "../store";

type Target = { kind: "agent"; agent: AgentView } | { kind: "channel"; meta: SessionMeta } | { kind: "folder"; name: string };

interface Open {
  x: number;
  y: number;
  target: Target;
}

let opener: ((o: Open) => void) | null = null;

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
  const [dialog, setDialog] = useState<{ kind: "rename" | "move" | "delete"; target: Target } | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);

  useEffect(() => {
    opener = setOpen;
    return () => {
      opener = null;
    };
  }, []);
  useEffect(() => {
    if (!open) return;
    const close = (e: Event) => {
      if (ref.current && e.target instanceof Node && ref.current.contains(e.target)) return;
      setOpen(null);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(null);
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", esc);
    window.addEventListener("blur", close);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", esc);
      window.removeEventListener("blur", close);
    };
  }, [open]);

  const folders = [...new Set([...Object.values(agents).map((a) => a.folder), ...Object.values(sessions).map((m) => m.folder)].filter(Boolean) as string[])].sort();

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
            {item(<Trash2 size={15} />, tg.kind === "agent" ? t("rm.deleteAgent") : t("rm.deleteChannel"), () => setDialog({ kind: "delete", target: tg }), true)}
          </>
        )}
      </div>
    );
  };

  return (
    <>
      {menu()}
      {dialog && <RowDialog d={dialog} folders={folders} onClose={() => setDialog(null)} />}
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

function RowDialog({ d, folders, onClose }: { d: { kind: "rename" | "move" | "delete"; target: Target }; folders: string[]; onClose: () => void }) {
  const tg = d.target;
  const current = tg.kind === "agent" ? tg.agent.folder ?? "" : tg.kind === "channel" ? tg.meta.folder ?? "" : tg.name;
  const [val, setVal] = useState(d.kind === "move" ? current : nameOf(tg));
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    input.current?.select();
  }, []);

  const submit = async () => {
    setBusy(true);
    await act(async () => {
      const v = val.trim();
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
        return t("rm.deletedAgent", { name: tg.agent.name });
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
    d.kind === "rename"
      ? tg.kind === "folder"
        ? t("rm.renameFolder")
        : t("rm.rename")
      : d.kind === "move"
        ? t("rm.moveTitle", { name: nameOf(tg) })
        : tg.kind === "agent"
          ? t("rm.deleteAgentQ", { name: nameOf(tg) })
          : tg.kind === "channel"
            ? t("rm.deleteChannelQ", { name: nameOf(tg) })
            : t("rm.dissolveQ", { name: nameOf(tg) });

  const note =
    d.kind !== "delete"
      ? ""
      : tg.kind === "agent"
        ? t("rm.deleteAgentNote")
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
              placeholder={d.kind === "move" ? t("rm.folderPh") : ""}
              onChange={(e) => setVal(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") void submit();
                if (e.key === "Escape") onClose();
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
            {d.kind === "delete" ? (tg.kind === "folder" ? t("rm.dissolveBtn") : t("rm.deleteBtn")) : t("common.save")}
          </button>
        </div>
        </div>
      </div>
    </>
  );
}
