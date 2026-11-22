import { useEffect, useMemo, useRef, useState } from "react";
import { Folder, Hash, X } from "lucide-react";
import { API, teamOf } from "../api";
import { t } from "../i18n";
import { openSession, refreshChannels, refreshSessions, setState, toast, useFolders, useStore, getState } from "../store";
import { Avatar } from "./Avatar";

/** 新建频道: name, members (first selected = owner), folder (docs/03 §5.2). */
export function NewChannel() {
  const open = useStore((s) => s.newChannel);
  if (!open) return null;
  return <Dialog />;
}

function Dialog() {
  const agents = useStore((s) => s.agents);
  const team = useStore((s) => s.team);
  const [name, setName] = useState("");
  const [members, setMembers] = useState<string[]>([]);
  const [folder, setFolder] = useState(() => getState().createIn);
  // handovers take effect at once unless this channel is one to watch
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  // a channel lives in one team: the team the sidebar shows
  const list = useMemo(() => Object.values(agents).filter((a) => a.kind !== "chat" && !a.ephemeral && teamOf(a) === team).sort((a, b) => a.name.localeCompare(b.name)), [agents, team]);
  const folders = useFolders();
  const close = () => setState({ newChannel: false, createIn: "" });

  useEffect(() => {
    nameRef.current?.focus();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        e.preventDefault(); // handled: the app's Esc leaves it alone
        close();
      }
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, []);

  const toggle = (id: string) => setMembers((m) => (m.includes(id) ? m.filter((x) => x !== id) : [...m, id]));

  const create = async () => {
    const n = name.trim().replace(/^#/, "").trim();
    if (!n) {
      setErr(t("nc.needName"));
      nameRef.current?.focus();
      return;
    }
    if (members.length === 0) {
      setErr(t("nc.needMember"));
      return;
    }
    setBusy(true);
    try {
      const ch = await API.createChannel({ name: n, members, folder: folder.trim() || undefined, handoverConfirm: confirm || undefined });
      setState((s) => ({ channels: { ...s.channels, [ch.id]: ch }, sessions: { ...s.sessions, [ch.id]: ch }, newChannel: false }));
      await Promise.allSettled([refreshSessions(), refreshChannels()]);
      openSession(ch.id);
      toast(t("nc.created", { name: n }));
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <div className="scrim" onClick={close} />
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="ncTitle">
        <div className="mhd">
          <h3 id="ncTitle">{t("list.newChannel")}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={close}>
            <X size={14} />
          </button>
        </div>
        <form
          className="mbd"
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <label className="fl" htmlFor="ncName">
            {t("nc.name")} <i>*</i>
          </label>
          <label className="fi sm iwrap">
            <Hash size={14} className="muted" />
            <input
              id="ncName"
              ref={nameRef}
              value={name}
              placeholder={t("nc.namePh")}
              onChange={(e) => {
                setName(e.target.value);
                setErr(null);
              }}
            />
          </label>
          <div className="fl">{t("nc.members")}</div>
          <p className="small muted ncnote">{t("nc.membersHint")}</p>
          <div className="mpick" role="listbox" aria-multiselectable="true">
            {list.length === 0 && <div className="small">{t("nc.noAgents")}</div>}
            {list.map((a) => {
              const i = members.indexOf(a.id);
              return (
                <button key={a.id} type="button" role="option" aria-selected={i >= 0} className="mopt" onClick={() => toggle(a.id)}>
                  {/* a checkbox (several can be picked) showing the pick order */}
                  <span className={"mbox" + (i >= 0 ? " on" : "")} aria-hidden>
                    {i >= 0 ? i + 1 : ""}
                  </span>
                  <Avatar agent={a} size={26} />
                  <span className="g">
                    <b>{a.name}</b>
                    <span>{a.role || a.id}</span>
                  </span>
                  {i === 0 && <span className="pill blue">{t("ch.owner")}</span>}
                  {i > 0 && (
                    <span
                      className="pill lead"
                      role="button"
                      tabIndex={0}
                      title={t("nc.makeOwner")}
                      onClick={(e) => {
                        e.stopPropagation();
                        setMembers((m) => [a.id, ...m.filter((x) => x !== a.id)]);
                      }}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          e.stopPropagation();
                          setMembers((m) => [a.id, ...m.filter((x) => x !== a.id)]);
                        }
                      }}
                    >
                      <span className="pl1">{t("nc.member")}</span>
                      <span className="pl2">{t("nc.makeOwner")}</span>
                    </span>
                  )}
                </button>
              );
            })}
          </div>
          <label className="fl" htmlFor="ncFolder">
            {t("na.folder")}
          </label>
          <label className="fi sm iwrap">
            <Folder size={14} className="muted" />
            <input id="ncFolder" list="ncFolders" value={folder} placeholder={t("na.folderPh")} onChange={(e) => setFolder(e.target.value)} />
            <datalist id="ncFolders">
              {folders.map((x) => (
                <option key={x} value={x} />
              ))}
            </datalist>
          </label>
          <div className="fl">{t("nc.handover")}</div>
          <div className="seg nchand" role="radiogroup" aria-label={t("nc.handover")}>
            <button type="button" aria-pressed={!confirm} onClick={() => setConfirm(false)}>
              {t("nc.handoverAuto")}
            </button>
            <button type="button" aria-pressed={confirm} onClick={() => setConfirm(true)}>
              {t("nc.handoverConfirm")}
            </button>
          </div>
          <p className="small muted ncnote">{t(confirm ? "nc.handoverConfirmHint" : "nc.handoverAutoHint")}</p>
          {err && <div className="ferr">{err}</div>}
          <div className="mft">
            <button className="btn ghost" type="button" onClick={close}>
              {t("common.cancel")}
            </button>
            <button className="btn pri" type="submit" disabled={busy}>
              {busy ? t("na.creating") : t("nc.create")}
            </button>
          </div>
        </form>
      </div>
    </>
  );
}
