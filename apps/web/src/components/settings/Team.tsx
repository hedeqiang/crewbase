import { Check, ChevronRight, Crown, Loader2, MoreHorizontal, Smartphone, Trash2, UserPlus, Users, X } from "lucide-react";
import { useEffect, useState } from "react";
import { API, type MemberView } from "../../api";
import { t, type Key } from "../../i18n";
import { openSettings, setState, toast, useStore } from "../../store";
import { Popover, useMenu } from "../Popover";
import { Head } from "../Settings";
import { PersonDot } from "./Teams";

const ROLES = ["admin", "member", "viewer"] as const;
type Role = (typeof ROLES)[number];

/** 设置 › 成员与角色: the people who share this Crewbase. A list first;
 *  adding someone is a dialog, and a member opens to what they may approve. */
export function Team() {
  const [list, setList] = useState<MemberView[] | null>(null);
  const [approvable, setApprovable] = useState<string[]>([]);
  const [ownerDevices, setOwnerDevices] = useState(0);
  const [err, setErr] = useState("");
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  const profile = useStore((s) => s.profile);

  const load = async () => {
    try {
      const r = await API.members();
      setList(r.members);
      setApprovable(r.approvable);
      setOwnerDevices(r.ownerDevices);
    } catch (e: any) {
      setErr(e?.status === 404 ? t("dev.old") : e?.message ?? String(e));
    }
  };
  useEffect(() => {
    void load();
  }, []);

  const patch = async (m: MemberView, b: { role?: string; approve?: string[] }) => {
    try {
      const r = await API.updateMember(m.id, b);
      setList((xs) => xs?.map((x) => (x.id === m.id ? { ...x, ...r } : x)) ?? null);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const remove = async (m: MemberView) => {
    if (!window.confirm(t("team.removeConfirm", { name: m.name, n: m.devices ?? 0 }))) return;
    try {
      await API.removeMember(m.id);
      toast(t("team.removed", { name: m.name }));
      void load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const pairFor = (m: MemberView) => {
    setState({ pairFor: m.id });
    openSettings("hosts");
  };

  return (
    <>
      <Head
        title={t("set.team")}
        sub={t("team.sub")}
        right={
          <button className="btn pri sm" type="button" onClick={() => setAdding(true)}>
            <UserPlus size={14} />
            {t("team.add")}
          </button>
        }
      />
      <div className="vb">
        {err && <p className="small errt">{err}</p>}
        <div className="tcards">
          <div className="tcard">
            <div className="tcardh static">
              <span className="pdot owner" style={{ width: 34, height: 34 }}>
                <Crown size={15} />
              </span>
              <span className="g">
                <b>
                  {profile.displayName || t("common.you")}
                  <span className="pill sm">{t("team.owner")}</span>
                </b>
                <span className="small muted">
                  {t("team.ownerDesc")}
                  {ownerDevices ? ` · ${t("team.devices", { n: ownerDevices })}` : ""}
                </span>
              </span>
            </div>
          </div>
          {list?.map((m) => (
            <MemberCard
              key={m.id}
              m={m}
              approvable={approvable}
              open={open === m.id}
              onToggle={() => setOpen(open === m.id ? null : m.id)}
              onPatch={(b) => void patch(m, b)}
              onPair={() => pairFor(m)}
              onRemove={() => void remove(m)}
            />
          ))}
        </div>
        {list && list.length === 0 && (
          <div className="tempty-big">
            <Users size={22} />
            <b>{t("team.emptyTitle")}</b>
            <span className="small muted">{t("team.empty")}</span>
            <button className="btn sm" type="button" onClick={() => setAdding(true)}>
              <UserPlus size={13} />
              {t("team.add")}
            </button>
          </div>
        )}
        <p className="small muted tnote">{t("team.note")}</p>
      </div>
      {adding && (
        <AddMemberDialog
          onClose={() => setAdding(false)}
          onAdded={async (m, pair) => {
            setAdding(false);
            await load();
            if (pair) pairFor(m);
            else setOpen(m.id);
          }}
        />
      )}
    </>
  );
}

function MemberCard({
  m,
  approvable,
  open,
  onToggle,
  onPatch,
  onPair,
  onRemove,
}: {
  m: MemberView;
  approvable: string[];
  open: boolean;
  onToggle: () => void;
  onPatch: (b: { role?: string; approve?: string[] }) => void;
  onPair: () => void;
  onRemove: () => void;
}) {
  const more = useMenu();
  const roleM = useMenu();
  const approves = m.role === "member";
  return (
    <div className={"tcard" + (open ? " open" : "")}>
      <div className="tcardh" role="button" tabIndex={0} aria-expanded={open} onClick={onToggle} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && onToggle()}>
        <PersonDot name={m.name} size={34} />
        <span className="g">
          <b>{m.name}</b>
          <span className="small muted">
            {t("team.devices", { n: m.devices ?? 0 })}
            {approves && " · " + t("team.approvesN", { n: m.approve.length })}
          </span>
        </span>
        <button
          className="rolebtn"
          type="button"
          ref={roleM.ref}
          onClick={(e) => {
            e.stopPropagation();
            roleM.toggle();
          }}
          aria-haspopup="menu"
        >
          {t(`team.role.${m.role}` as Key)}
          <ChevronRight size={12} className="rot90" />
        </button>
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
        <ChevronRight size={15} className="tchev" />
      </div>
      <Popover anchor={roleM.anchor} open={roleM.open} onClose={roleM.close} width={270} align="end">
        {ROLES.map((r) => (
          <button
            key={r}
            className="mi"
            type="button"
            onClick={() => {
              roleM.close();
              if (r !== m.role) onPatch({ role: r });
            }}
          >
            <span className="g">
              {t(`team.role.${r}` as Key)}
              <span>{t(`team.roleDesc.${r}` as Key)}</span>
            </span>
            <span className="ck">{r === m.role && <Check size={14} />}</span>
          </button>
        ))}
      </Popover>
      <Popover anchor={more.anchor} open={more.open} onClose={more.close} width={200} align="end">
        <button
          className="mi"
          type="button"
          onClick={() => {
            more.close();
            onPair();
          }}
        >
          <Smartphone size={15} />
          <span className="g">{t("team.pairFor", { name: m.name })}</span>
        </button>
        <button
          className="mi dng"
          type="button"
          onClick={() => {
            more.close();
            onRemove();
          }}
        >
          <Trash2 size={15} />
          <span className="g">{t("team.remove")}</span>
        </button>
      </Popover>
      {open && (
        <div className="tbody">
          <p className="small muted" style={{ margin: "0 0 10px" }}>
            {t(`team.roleDesc.${m.role}` as Key)}
          </p>
          {approves ? (
            <>
              <div className="small" style={{ marginBottom: 6 }}>
                {t("team.approves")}
              </div>
              <div className="tcats">
                {approvable.map((c) => {
                  const on = m.approve.includes(c);
                  return (
                    <button
                      key={c}
                      type="button"
                      className={"tcat" + (on ? " on" : "")}
                      aria-pressed={on}
                      onClick={() => onPatch({ approve: on ? m.approve.filter((x) => x !== c) : [...m.approve, c] })}
                    >
                      {on && <Check size={11} />}
                      {t(`cat.${c}` as Key)}
                    </button>
                  );
                })}
              </div>
              <p className="small muted" style={{ margin: "8px 0 0" }}>
                {t("team.paymentNever")}
              </p>
            </>
          ) : (
            <p className="small muted" style={{ margin: 0 }}>
              {m.role === "admin" ? t("team.adminApproves") : t("team.viewerApproval")}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function AddMemberDialog({ onClose, onAdded }: { onClose: () => void; onAdded: (m: MemberView, pair: boolean) => void }) {
  const [name, setName] = useState("");
  const [role, setRole] = useState<Role>("member");
  const [busy, setBusy] = useState(false);
  const add = async (pair: boolean) => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      const m = await API.addTeamMember({ name: name.trim(), role });
      toast(t("team.added", { name: m.name }));
      onAdded(m, pair);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal" role="dialog" aria-modal="true" aria-labelledby="amTitle">
        <div className="mhd">
          <h3 id="amTitle">{t("team.add")}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={15} />
          </button>
        </div>
        <form
          className="mbd"
          onSubmit={(e) => {
            e.preventDefault();
            void add(true);
          }}
        >
          <label className="fl" htmlFor="amName">
            {t("team.name")}
          </label>
          <input id="amName" className="fi" autoFocus value={name} maxLength={30} placeholder={t("team.namePh")} onChange={(e) => setName(e.target.value)} />
          <label className="fl">{t("team.roleLabel")}</label>
          <div className="ntvis" role="radiogroup">
            {ROLES.map((r) => (
              <button key={r} type="button" role="radio" aria-checked={role === r} className={"ntopt" + (role === r ? " on" : "")} onClick={() => setRole(r)}>
                <span className="g">
                  <b>{t(`team.role.${r}` as Key)}</b>
                  <span>{t(`team.roleDesc.${r}` as Key)}</span>
                </span>
              </button>
            ))}
          </div>
          <p className="small muted" style={{ margin: "10px 0 0" }}>
            {t("team.addHint")}
          </p>
          <div className="mft">
            <button className="btn" type="button" disabled={busy || !name.trim()} onClick={() => void add(false)}>
              {t("team.addOnly")}
            </button>
            <button className="btn pri" type="submit" disabled={busy || !name.trim()}>
              {busy ? <Loader2 size={13} className="spin" /> : <Smartphone size={13} />}
              {t("team.addAndPair")}
            </button>
          </div>
        </form>
      </div>
    </>
  );
}
