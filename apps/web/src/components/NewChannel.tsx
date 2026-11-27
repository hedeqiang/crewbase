import { useEffect, useMemo, useRef, useState } from "react";
import { Boxes, Folder, Hash, Users } from "lucide-react";
import { API, teamOf } from "../api";
import { t } from "../i18n";
import { openSession, refreshAgents, refreshChannels, refreshSessions, setState, toast, useFolders, useStore, getState } from "../store";
import { Avatar } from "./Avatar";
import { RoundtablePicker, createSeats, defaultSeats, makersOf, type Seat } from "./Roundtable";

/** 新建频道: name, members (first selected = owner), folder (docs/03 §5.2). */
/** 新建频道: a page in the main column (#/new-channel), like 新建 Agent. */
export function NewChannel() {
  const agents = useStore((s) => s.agents);
  const team = useStore((s) => s.team);
  const [name, setName] = useState("");
  const [members, setMembers] = useState<string[]>([]);
  const [folder, setFolder] = useState(() => getState().createIn);
  // handovers take effect at once unless this channel is one to watch
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // 选成员 (existing agents) or 模型圆桌 (new members, one per model)
  const [kind, setKind] = useState<"members" | "roundtable">("members");
  const models = useStore((s) => s.models);
  const defaults = useStore((s) => s.profile.defaultModels);
  const makers = useMemo(() => makersOf(models, defaults ?? []), [models, defaults]);
  const [seats, setSeats] = useState<Seat[] | null>(null);
  const table = seats ?? defaultSeats(models, makers);
  const [made, setMade] = useState(0);
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

  const toggle = (id: string) => {
    setErr(null);
    setMembers((m) => (m.includes(id) ? m.filter((x) => x !== id) : [...m, id]));
  };

  const create = async () => {
    const n = name.trim().replace(/^#/, "").trim();
    if (!n) {
      setErr(t("nc.needName"));
      nameRef.current?.focus();
      return;
    }
    const round = kind === "roundtable";
    if (!round && members.length === 0) {
      setErr(t("nc.needMember"));
      return;
    }
    if (round && table.length === 0) {
      setErr(t("rt.needModel"));
      return;
    }
    if (round && table.some((s) => !s.name.trim())) {
      setErr(t("na.needName"));
      return;
    }
    setBusy(true);
    try {
      let ids = members;
      if (round) {
        ids = await createSeats(models, table, folder.trim() || undefined, setMade);
        await refreshAgents();
      }
      const ch = await API.createChannel({ name: n, members: ids, folder: folder.trim() || undefined, handoverConfirm: confirm || undefined });
      setState((s) => ({ channels: { ...s.channels, [ch.id]: ch }, sessions: { ...s.sessions, [ch.id]: ch }, newChannel: false }));
      await Promise.allSettled([refreshSessions(), refreshChannels()]);
      openSession(ch.id);
      toast(round ? t("rt.created", { name: n, n: ids.length }) : t("nc.created", { name: n }));
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
      setMade(0);
    }
  };

  return (
    <div className="ncpage">
      <div className="nccard" role="region" aria-labelledby="ncTitle">
        <div className="mhd">
          <h3 id="ncTitle">{t("list.newChannel")}</h3>
        </div>
        <form
          className="mbd"
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <div className="seg nckind" role="radiogroup" aria-label={t("nc.kind")}>
            <button
              type="button"
              role="radio"
              aria-checked={kind === "members"}
              aria-pressed={kind === "members"}
              onClick={() => {
                setKind("members");
                setErr(null);
                if (name === t("rt.defaultName")) setName("");
              }}
            >
              <Users size={13} />
              {t("nc.kindMembers")}
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={kind === "roundtable"}
              aria-pressed={kind === "roundtable"}
              onClick={() => {
                setKind("roundtable");
                setErr(null);
                if (!name.trim()) setName(t("rt.defaultName"));
              }}
            >
              <Boxes size={13} />
              {t("nc.kindRoundtable")}
            </button>
          </div>
          <label className="fl" htmlFor="ncName">
            <span>
              {t("nc.name")} <i>*</i>
            </span>
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
          {kind === "roundtable" ? (
            <>
              <div className="fl rtfl">
                {t("rt.models")} <span className="muted rtcount">{t("rt.count", { n: table.length })}</span>
              </div>
              <p className="small muted ncnote">{t("rt.hint")}</p>
              <RoundtablePicker
                models={models}
                makers={makers}
                seats={table}
                setSeats={(s) => {
                  setSeats(s);
                  setErr(null);
                }}
              />
            </>
          ) : (
          <>
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
          </>
          )}
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
              {busy ? (made ? t("rt.creating", { n: made, total: table.length }) : t("na.creating")) : kind === "roundtable" ? t("rt.create", { n: table.length }) : t("nc.create")}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
