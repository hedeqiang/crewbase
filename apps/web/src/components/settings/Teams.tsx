import { Globe, Lock, Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { API, DEFAULT_TEAM, teamOf, type TeamView, type TeamVisibility } from "../../api";
import { t } from "../../i18n";
import { refreshAgents, refreshTeams, switchTeam, toast, useStore } from "../../store";
import { Avatar } from "../Avatar";
import { Head } from "../Settings";

const COLORS = ["#3b32c4", "#24b07e", "#f2b33d", "#e5484d", "#3987e5", "#d651c8", "#1fb5a8", "#ef8a45"];

/** 设置 › 团队: teams group agents and their channels; the teammate roster,
 *  delegation and channel members stay inside a team. */
export function Teams() {
  const teams = useStore((s) => s.teams);
  const current = useStore((s) => s.team);
  const agents = useStore((s) => s.agents);
  const [name, setName] = useState("");
  const [vis, setVis] = useState<TeamVisibility>("public");
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState("");

  const run = async (fn: () => Promise<unknown>, ok?: string) => {
    setBusy(true);
    try {
      await fn();
      if (ok) toast(ok);
      await Promise.all([refreshTeams(), refreshAgents()]);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

  const create = () =>
    run(async () => {
      const tm = await API.createTeam({ name: name.trim(), visibility: vis, color: COLORS[teams.length % COLORS.length] });
      setName("");
      switchTeam(tm.id);
    }, t("teams.created", { name: name.trim() }));

  const rename = (tm: TeamView) => {
    const n = draft.trim();
    setEditing(null);
    if (n && n !== tm.name) void run(() => API.patchTeam(tm.id, { name: n }));
  };

  const remove = (tm: TeamView) => {
    if (tm.agents || tm.channels) {
      toast(t("teams.notEmpty", { name: tm.name }));
      return;
    }
    if (!window.confirm(t("teams.deleteConfirm", { name: tm.name }))) return;
    void run(async () => {
      await API.deleteTeam(tm.id);
      if (current === tm.id) switchTeam(DEFAULT_TEAM);
    });
  };

  const members = (tm: TeamView) => Object.values(agents).filter((a) => a.kind !== "chat" && teamOf(a) === tm.id);

  return (
    <>
      <Head title={t("set.teams")} sub={t("teams.sub")} />
      <div className="vb">
        <div className="vsec">{t("teams.new")}</div>
        <div className="sbox">
          <form
            className="teamnew"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim()) void create();
            }}
          >
            <input className="fi" value={name} maxLength={40} onChange={(e) => setName(e.target.value)} placeholder={t("teams.namePh")} />
            <div className="seg">
              {(["public", "private"] as const).map((v) => (
                <button key={v} type="button" aria-pressed={vis === v} onClick={() => setVis(v)} title={t(`teams.${v}Desc`)}>
                  {v === "public" ? <Globe size={13} /> : <Lock size={13} />}
                  {t(`teams.${v}`)}
                </button>
              ))}
            </div>
            <button className="btn pri" type="submit" disabled={busy || !name.trim()}>
              <Plus size={14} />
              {t("teams.create")}
            </button>
          </form>
          <p className="small" style={{ margin: "0 14px 12px" }}>
            {t(vis === "public" ? "teams.publicDesc" : "teams.privateDesc")}
          </p>
        </div>

        <div className="vsec">{t("teams.title")}</div>
        {teams.map((tm) => (
          <div className="sbox teamcard" key={tm.id}>
            <div className="teamh">
              <span className="tdot lg" style={{ background: tm.color || "var(--accent)" }}>
                {tm.name.slice(0, 1)}
              </span>
              <span className="g">
                {editing === tm.id ? (
                  <input
                    className="fi"
                    autoFocus
                    value={draft}
                    maxLength={40}
                    onChange={(e) => setDraft(e.target.value)}
                    onBlur={() => rename(tm)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") rename(tm);
                      if (e.key === "Escape") setEditing(null);
                    }}
                  />
                ) : (
                  <b>
                    {tm.name}
                    {tm.default && <span className="pill sm">{t("teams.default")}</span>}
                    {tm.id === current && <span className="pill sm on">{t("teams.showing")}</span>}
                  </b>
                )}
                <span className="small">{t("teams.counts", { agents: tm.agents, channels: tm.channels })}</span>
              </span>
              <div className="seg sm">
                {(["public", "private"] as const).map((v) => (
                  <button
                    key={v}
                    type="button"
                    aria-pressed={tm.visibility === v}
                    disabled={busy}
                    title={t(`teams.${v}Desc`)}
                    onClick={() => tm.visibility !== v && void run(() => API.patchTeam(tm.id, { visibility: v }))}
                  >
                    {v === "public" ? <Globe size={12} /> : <Lock size={12} />}
                    {t(`teams.${v}`)}
                  </button>
                ))}
              </div>
              {tm.id !== current && (
                <button className="btn sm" type="button" onClick={() => switchTeam(tm.id)}>
                  {t("teams.open")}
                </button>
              )}
              <button
                className="ix"
                type="button"
                title={t("teams.rename")}
                aria-label={t("teams.rename")}
                onClick={() => {
                  setDraft(tm.name);
                  setEditing(tm.id);
                }}
              >
                <Pencil size={13} />
              </button>
              {!tm.default && (
                <button className="ix dng" type="button" title={t("common.delete")} aria-label={t("common.delete")} onClick={() => remove(tm)}>
                  <Trash2 size={13} />
                </button>
              )}
            </div>
            {members(tm).length > 0 && (
              <div className="teamagents">
                {members(tm).map((a) => (
                  <div className="teamagent" key={a.id}>
                    <Avatar agent={a} size={22} />
                    <span className="g">
                      <b>{a.name}</b>
                      {a.role && <span className="small">{a.role}</span>}
                    </span>
                    {teams.length > 1 && (
                      <span className="ssel">
                        <select
                          value={tm.id}
                          aria-label={t("teams.moveTo", { name: a.name })}
                          disabled={busy}
                          onChange={(e) => {
                            const to = teams.find((x) => x.id === e.target.value);
                            if (to) void run(() => API.moveToTeam(to.id, a.id), t("teams.moved", { name: a.name, team: to.name }));
                          }}
                        >
                          {teams.map((x) => (
                            <option key={x.id} value={x.id}>
                              {x.id === tm.id ? t("teams.inTeam", { team: x.name }) : t("teams.moveOption", { team: x.name })}
                            </option>
                          ))}
                        </select>
                      </span>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
        <p className="small" style={{ marginTop: 8 }}>
          {t("teams.note")}
        </p>
      </div>
    </>
  );
}
