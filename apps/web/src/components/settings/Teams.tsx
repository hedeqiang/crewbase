import { Check, ChevronRight, Globe, Lock, MoreHorizontal, Pencil, Plus, Trash2, X } from "lucide-react";
import { useEffect, useState } from "react";
import { API, DEFAULT_TEAM, teamOf, type MemberView, type TeamRole, type TeamView, type TeamVisibility } from "../../api";
import { t, type Key } from "../../i18n";
import { refreshAgents, refreshTeams, switchTeam, toast, useStore } from "../../store";
import { kfmt, modeLabel } from "../../format";
import { Avatar } from "../Avatar";
import { Popover, useMenu } from "../Popover";
import { Head } from "../Settings";
import { confirmDialog } from "../Confirm";

const COLORS = ["#3b32c4", "#24b07e", "#f2b33d", "#e5484d", "#3987e5", "#d651c8", "#1fb5a8", "#ef8a45"];

/** A person's initial in a round tile (people are not agents: no eyes). */
export function PersonDot({ name, size = 26 }: { name: string; size?: number }) {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  const bg = ["#6b7280", "#8b7cf6", "#24b07e", "#ef8a45", "#3987e5", "#d651c8", "#1fb5a8"][h % 7];
  return (
    <span className="pdot" style={{ width: size, height: size, background: bg, fontSize: Math.round(size * 0.42) }} title={name}>
      {Array.from(name.trim())[0]?.toUpperCase() ?? "?"}
    </span>
  );
}

async function run(fn: () => Promise<unknown>, ok?: string) {
  try {
    await fn();
    if (ok) toast(ok);
    await Promise.all([refreshTeams(), refreshAgents()]);
    return true;
  } catch (e: any) {
    toast(e?.message ?? String(e));
    return false;
  }
}

/** 设置 › 团队: the teams on this host as cards; a card opens to its people,
 *  agents and settings. Creating a team is a dialog, not a standing form. */
export function Teams() {
  const teams = useStore((s) => s.teams);
  const current = useStore((s) => s.team);
  const [open, setOpen] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [people, setPeople] = useState<MemberView[]>([]);
  const myName = useStore((s) => s.profile.displayName);
  useEffect(() => {
    API.members().then(
      (r) => setPeople(r.members),
      () => setPeople([]),
    );
  }, []);
  const personName = (id: string) => (id === "owner" ? myName || t("teams.ownerName") : people.find((p) => p.id === id)?.name ?? t("teams.gone"));

  return (
    <>
      <Head
        title={t("set.teams")}
        sub={t("teams.sub")}
        right={
          <button className="btn pri sm" type="button" onClick={() => setCreating(true)}>
            <Plus size={14} />
            {t("teams.new")}
          </button>
        }
      />
      <div className="vb">
        <div className="tcards">
          {teams.map((tm) => (
            <TeamCard
              key={tm.id}
              tm={tm}
              open={open === tm.id}
              current={current === tm.id}
              onToggle={() => setOpen(open === tm.id ? null : tm.id)}
              people={people}
              personName={personName}
            />
          ))}
        </div>
        <p className="small muted tnote">{t("teams.note")}</p>
      </div>
      {creating && (
        <NewTeamDialog
          onClose={() => setCreating(false)}
          onCreated={(id) => {
            setCreating(false);
            setOpen(id);
          }}
        />
      )}
    </>
  );
}

function TeamCard({
  tm,
  open,
  current,
  onToggle,
  people,
  personName,
}: {
  tm: TeamView;
  open: boolean;
  current: boolean;
  onToggle: () => void;
  people: MemberView[];
  personName: (id: string) => string;
}) {
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  const mine = Object.values(agents).filter((a) => a.kind !== "chat" && !a.ephemeral && teamOf(a) === tm.id);
  const more = useMenu();
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(tm.name);
  const [tab, setTab] = useState<"people" | "agents" | "settings">("people");
  const setTeamPeople = (ps: { id: string; role: TeamRole }[]) => void run(() => API.patchTeam(tm.id, { people: ps }));
  const rename = () => {
    setRenaming(false);
    const n = draft.trim();
    if (n && n !== tm.name) void run(() => API.patchTeam(tm.id, { name: n }));
  };
  const remove = async () => {
    more.close();
    if (tm.agents || tm.channels) return toast(t("teams.notEmpty", { name: tm.name }));
    if (await confirmDialog(t("teams.deleteConfirm", { name: tm.name })))
      void run(async () => {
        await API.deleteTeam(tm.id);
        if (current) switchTeam(DEFAULT_TEAM);
      });
  };

  return (
    <div className={"tcard" + (open ? " open" : "")}>
      <div className="tcardh" role="button" tabIndex={0} aria-expanded={open} onClick={onToggle} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onToggle()}>
        <span className="tdot lg" style={{ background: tm.color || "var(--accent)" }}>
          {tm.name.slice(0, 1)}
        </span>
        <span className="g">
          {renaming ? (
            <input
              className="fi sm"
              autoFocus
              value={draft}
              maxLength={40}
              onClick={(e) => e.stopPropagation()}
              onChange={(e) => setDraft(e.target.value)}
              onBlur={rename}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === "Enter") rename();
                if (e.key === "Escape") setRenaming(false);
              }}
            />
          ) : (
            <b>
              {tm.name}
              {tm.default && <span className="pill sm">{t("teams.default")}</span>}
              {current && <span className="pill sm blue">{t("teams.showing")}</span>}
            </b>
          )}
          <span className="small muted tmeta">
            {tm.visibility === "private" ? <Lock size={11} /> : <Globe size={11} />}
            {t(`teams.${tm.visibility}`)} · {t("teams.counts", { agents: tm.agents, channels: tm.channels })}
            {tm.tokens > 0 && " · " + t("teams.tokens", { n: kfmt(tm.tokens) })}
          </span>
        </span>
        <span className="tstack" aria-label={t("teams.people", { n: tm.people.length })}>
          {tm.people.slice(0, 4).map((p) => (
            <PersonDot key={p.id} name={personName(p.id)} size={22} />
          ))}
          {tm.people.length > 4 && <span className="pdot more">+{tm.people.length - 4}</span>}
        </span>
        {!current && (
          <button
            className="btn sm"
            type="button"
            onClick={(e) => {
              e.stopPropagation();
              switchTeam(tm.id);
            }}
          >
            {t("teams.open")}
          </button>
        )}
        {tm.canManage && (
          <button
            className="ix"
            type="button"
            ref={more.ref}
            aria-label={t("rm.more")}
            onClick={(e) => {
              e.stopPropagation();
              more.toggle();
            }}
          >
            <MoreHorizontal size={15} />
          </button>
        )}
        <ChevronRight size={15} className="tchev" />
      </div>
      <Popover anchor={more.anchor} open={more.open} onClose={more.close} width={180} align="end">
        <button
          className="mi"
          type="button"
          onClick={() => {
            more.close();
            setDraft(tm.name);
            setRenaming(true);
          }}
        >
          <Pencil size={15} />
          <span className="g">{t("teams.rename")}</span>
        </button>
        {!tm.default && (
          <button className="mi dng" type="button" onClick={remove}>
            <Trash2 size={15} />
            <span className="g">{t("common.delete")}</span>
          </button>
        )}
      </Popover>

      {open && (
        <div className="tbody">
          <div className="seg sm ttabs" role="tablist">
            {(["people", "agents", "settings"] as const).map((k) => (
              <button key={k} type="button" role="tab" aria-pressed={tab === k} onClick={() => setTab(k)}>
                {t(`teams.tab.${k}` as Key)}
                {k === "people" && <span className="cnt">{tm.people.length}</span>}
                {k === "agents" && <span className="cnt">{mine.length}</span>}
              </button>
            ))}
          </div>

          {tab === "people" && (
            <div className="tlist">
              {tm.people.map((pp) => (
                <div className="trow" key={pp.id}>
                  <PersonDot name={personName(pp.id)} />
                  <span className="g">
                    <b>{personName(pp.id)}</b>
                    <span className="small muted">{t(`teams.roleDesc.${pp.role}` as Key)}</span>
                  </span>
                  <RolePicker
                    value={pp.role}
                    disabled={!tm.canManage}
                    onChange={(r) => setTeamPeople(tm.people.map((x) => (x.id === pp.id ? { ...x, role: r } : x)))}
                  />
                  {tm.canManage && (
                    <button
                      className="ix dng"
                      type="button"
                      title={t("teams.removePerson")}
                      aria-label={t("teams.removePerson")}
                      disabled={pp.role === "lead" && tm.people.filter((x) => x.role === "lead").length === 1}
                      onClick={() => setTeamPeople(tm.people.filter((x) => x.id !== pp.id))}
                    >
                      <X size={13} />
                    </button>
                  )}
                </div>
              ))}
              {tm.canManage && <AddPerson tm={tm} people={people} personName={personName} onAdd={(id) => setTeamPeople([...tm.people, { id, role: "member" }])} />}
            </div>
          )}

          {tab === "agents" && (
            <div className="tlist">
              {mine.length === 0 && <p className="small muted tempty">{t("teams.noAgents")}</p>}
              {mine.map((a) => (
                <div className="trow" key={a.id}>
                  <Avatar agent={a} size={26} />
                  <span className="g">
                    <b>{a.name}</b>
                    {a.role && <span className="small muted">{a.role}</span>}
                  </span>
                  {teams.length > 1 && tm.canManage && (
                    <span className="ssel">
                      <select
                        value=""
                        aria-label={t("teams.moveTo", { name: a.name })}
                        onChange={(e) => {
                          const to = teams.find((x) => x.id === e.target.value);
                          if (to) void run(() => API.moveToTeam(to.id, a.id), t("teams.moved", { name: a.name, team: to.name }));
                        }}
                      >
                        <option value="">{t("teams.moveShort")}</option>
                        {teams
                          .filter((x) => x.id !== tm.id)
                          .map((x) => (
                            <option key={x.id} value={x.id}>
                              {x.name}
                            </option>
                          ))}
                      </select>
                    </span>
                  )}
                </div>
              ))}
            </div>
          )}

          {tab === "settings" && <TeamSettings tm={tm} />}
        </div>
      )}
    </div>
  );
}

function RolePicker({ value, disabled, onChange }: { value: TeamRole; disabled?: boolean; onChange: (r: TeamRole) => void }) {
  const m = useMenu();
  return (
    <>
      <button className="rolebtn" type="button" ref={m.ref} disabled={disabled} onClick={m.toggle} aria-haspopup="menu">
        {t(`teams.role.${value}`)}
        {!disabled && <ChevronRight size={12} className="rot90" />}
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} width={250} align="end">
        {(["lead", "member", "viewer"] as const).map((r) => (
          <button
            key={r}
            className="mi"
            type="button"
            onClick={() => {
              m.close();
              if (r !== value) onChange(r);
            }}
          >
            <span className="g">
              {t(`teams.role.${r}`)}
              <span>{t(`teams.roleDesc.${r}` as Key)}</span>
            </span>
            <span className="ck">{r === value && <Check size={14} />}</span>
          </button>
        ))}
      </Popover>
    </>
  );
}

function AddPerson({ tm, people, personName, onAdd }: { tm: TeamView; people: MemberView[]; personName: (id: string) => string; onAdd: (id: string) => void }) {
  const m = useMenu();
  const left = [{ id: "owner", name: personName("owner") }, ...people].filter((p) => !tm.people.some((x) => x.id === p.id));
  return (
    <>
      <button className="taddrow" type="button" ref={m.ref} onClick={m.toggle} disabled={left.length === 0}>
        <Plus size={14} />
        {left.length === 0 ? t("teams.everyoneIn") : t("teams.addPerson")}
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} width={240}>
        <div className="mlbl">{t("teams.addPerson")}</div>
        {left.map((p) => (
          <button
            key={p.id}
            className="mi"
            type="button"
            onClick={() => {
              m.close();
              onAdd(p.id);
            }}
          >
            <PersonDot name={p.name} size={20} />
            <span className="g">{p.name}</span>
          </button>
        ))}
      </Popover>
    </>
  );
}

function TeamSettings({ tm }: { tm: TeamView }) {
  const [text, setText] = useState<string | null>(null);
  const [saved, setSaved] = useState("");
  useEffect(() => {
    API.teamMemory(tm.id).then(
      (r) => {
        setText(r.text);
        setSaved(r.text);
      },
      () => setText(""),
    );
  }, [tm.id]);
  return (
    <div className="tsettings">
      <div className="tset">
        <span className="g">
          <b>{t("teams.visibility")}</b>
          <span className="small muted">{t(tm.visibility === "public" ? "teams.publicDesc" : "teams.privateDesc")}</span>
        </span>
        <div className="seg sm">
          {(["public", "private"] as const).map((v) => (
            <button key={v} type="button" aria-pressed={tm.visibility === v} disabled={!tm.canManage} onClick={() => tm.visibility !== v && void run(() => API.patchTeam(tm.id, { visibility: v }))}>
              {v === "public" ? <Globe size={12} /> : <Lock size={12} />}
              {t(`teams.${v}`)}
            </button>
          ))}
        </div>
      </div>
      <div className="tset">
        <span className="g">
          <b>{t("teams.defaultMode")}</b>
          <span className="small muted">{t("teams.defaultModeDesc")}</span>
        </span>
        <div className="seg sm">
          {(["sandbox", "read-only"] as const).map((m) => (
            <button
              key={m}
              type="button"
              aria-pressed={(tm.defaultMode || "sandbox") === m}
              disabled={!tm.canManage}
              onClick={() => void run(() => API.patchTeam(tm.id, { defaultMode: m === "sandbox" ? "" : m }))}
            >
              {modeLabel(m)}
            </button>
          ))}
        </div>
      </div>
      <div className="tset col">
        <span className="g">
          <b>{t("teams.memory")}</b>
          <span className="small muted">{t("teams.memoryDesc")}</span>
        </span>
        {text !== null && (
          <>
            <textarea className="fi mono" rows={6} value={text} readOnly={!tm.canManage} placeholder={t("teams.memoryPh")} onChange={(e) => setText(e.target.value)} />
            {tm.canManage && text !== saved && (
              <div className="tsave">
                <button className="btn sm" type="button" onClick={() => setText(saved)}>
                  {t("common.cancel")}
                </button>
                <button
                  className="btn pri sm"
                  type="button"
                  onClick={() =>
                    void run(async () => {
                      const r = await API.setTeamMemory(tm.id, text);
                      setSaved(r.text);
                    }, t("teams.memorySaved"))
                  }
                >
                  {t("common.save")}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function NewTeamDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (id: string) => void }) {
  const teams = useStore((s) => s.teams);
  const [name, setName] = useState("");
  const [vis, setVis] = useState<TeamVisibility>("public");
  const [color, setColor] = useState(COLORS[teams.length % COLORS.length]);
  const [busy, setBusy] = useState(false);
  const create = async () => {
    if (!name.trim()) return;
    setBusy(true);
    let id = "";
    const ok = await run(async () => {
      const tm = await API.createTeam({ name: name.trim(), visibility: vis, color });
      id = tm.id;
      switchTeam(tm.id);
    }, t("teams.created", { name: name.trim() }));
    setBusy(false);
    if (ok) onCreated(id);
  };
  return (
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="ntTitle">
        <div className="mhd">
          <h3 id="ntTitle">{t("teams.new")}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={15} />
          </button>
        </div>
        <form
          className="mbd"
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <div className="ntprev">
            <span className="tdot xl" style={{ background: color }}>
              {name.trim().slice(0, 1) || "?"}
            </span>
          </div>
          <input className="fi" autoFocus value={name} maxLength={40} onChange={(e) => setName(e.target.value)} placeholder={t("teams.namePh")} />
          <div className="ntcolors">
            {COLORS.map((c) => (
              <button key={c} type="button" className="dotc" style={{ ["--c" as any]: c }} aria-pressed={color === c} aria-label={c} onClick={() => setColor(c)} />
            ))}
          </div>
          <div className="ntvis" role="radiogroup">
            {(["public", "private"] as const).map((v) => (
              <button key={v} type="button" role="radio" aria-checked={vis === v} className={"ntopt" + (vis === v ? " on" : "")} onClick={() => setVis(v)}>
                {v === "public" ? <Globe size={16} /> : <Lock size={16} />}
                <span className="g">
                  <b>{t(`teams.${v}`)}</b>
                  <span>{t(`teams.${v}Desc`)}</span>
                </span>
              </button>
            ))}
          </div>
          <div className="mft">
            <button className="btn" type="button" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button className="btn pri" type="submit" disabled={busy || !name.trim()}>
              {t("teams.create")}
            </button>
          </div>
        </form>
      </div>
    </>
  );
}
