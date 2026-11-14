import { Crown, Loader2, Smartphone, Trash2, UserPlus } from "lucide-react";
import { useEffect, useState } from "react";
import { API, type MemberView } from "../../api";
import { t, type Key } from "../../i18n";
import { openSettings, setState, toast, useStore } from "../../store";
import { AvatarRaw, colorFor } from "../Avatar";
import { Head } from "../Settings";

const ROLES = ["admin", "member", "viewer"] as const;

/** 设置 › 成员与角色: people who share this Crewbase, and what they may do. */
export function Team() {
  const [list, setList] = useState<MemberView[] | null>(null);
  const [approvable, setApprovable] = useState<string[]>([]);
  const [ownerDevices, setOwnerDevices] = useState(0);
  const [name, setName] = useState("");
  const [role, setRole] = useState<(typeof ROLES)[number]>("member");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
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

  const add = async () => {
    if (!name.trim()) return;
    setBusy(true);
    try {
      await API.addTeamMember({ name: name.trim(), role });
      setName("");
      void load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };

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
      <Head title={t("set.team")} />
      <div className="vb">
        {err && <p className="small errt">{err}</p>}
        <div className="htable team">
          <div className="hrow teamrow">
            <span className="tav owner">
              <Crown size={14} />
            </span>
            <span className="g">
              <b>{profile.displayName || t("team.owner")}</b>
              <span>
                {t("team.ownerDesc")}
                {ownerDevices ? ` · ${t("team.devices", { n: ownerDevices })}` : ""}
              </span>
            </span>
          </div>
          {list?.map((m) => (
            <div key={m.id} className="hrow teamrow">
              <AvatarRaw color={colorFor(m.id)} name={m.name} size={30} shape="circle" />
              <span className="g">
                <b>{m.name}</b>
                <span>
                  {t(`team.roleDesc.${m.role}` as Key)} · {t("team.devices", { n: m.devices ?? 0 })}
                </span>
                {m.role !== "viewer" && m.role !== "admin" && (
                  <span className="tcats" aria-label={t("team.approves")}>
                    <span className="small">{t("team.approves")}</span>
                    {approvable.map((c) => {
                      const on = m.approve.includes(c);
                      return (
                        <button
                          key={c}
                          type="button"
                          className={"tcat" + (on ? " on" : "")}
                          aria-pressed={on}
                          onClick={() => void patch(m, { approve: on ? m.approve.filter((x) => x !== c) : [...m.approve, c] })}
                        >
                          {t(`cat.${c}` as Key)}
                        </button>
                      );
                    })}
                  </span>
                )}
              </span>
              <span className="ssel">
                <select value={m.role} aria-label={t("set.team")} onChange={(e) => void patch(m, { role: e.target.value })}>
                  {ROLES.map((r) => (
                    <option key={r} value={r}>
                      {t(`team.role.${r}` as Key)}
                    </option>
                  ))}
                </select>
              </span>
              <button className="ix" type="button" title={t("team.pairFor", { name: m.name })} aria-label={t("team.pairFor", { name: m.name })} onClick={() => pairFor(m)}>
                <Smartphone size={14} />
              </button>
              <button className="ix dng" type="button" title={t("team.remove")} aria-label={t("team.remove")} onClick={() => void remove(m)}>
                <Trash2 size={13} />
              </button>
            </div>
          ))}
        </div>
        {list && list.length === 0 && <p className="small devempty">{t("team.empty")}</p>}

        <form
          className="teamadd"
          onSubmit={(e) => {
            e.preventDefault();
            void add();
          }}
        >
          <input className="fi sm" value={name} maxLength={30} placeholder={t("team.namePh")} onChange={(e) => setName(e.target.value)} />
          <span className="ssel">
            <select value={role} onChange={(e) => setRole(e.target.value as (typeof ROLES)[number])}>
              {ROLES.map((r) => (
                <option key={r} value={r}>
                  {t(`team.role.${r}` as Key)}
                </option>
              ))}
            </select>
          </span>
          <button className="btn pri sm" type="submit" disabled={busy || !name.trim()}>
            {busy ? <Loader2 size={13} className="spin" /> : <UserPlus size={13} />} {t("team.add")}
          </button>
        </form>
        <p className="small" style={{ marginTop: 12 }}>
          {t("team.note")}
        </p>
      </div>
    </>
  );
}
