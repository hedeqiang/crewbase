import { CheckCircle2, ExternalLink, Loader2, MessageCircle, Send, Trash2, X } from "lucide-react";
import { useEffect, useState } from "react";
import { API, type ImGateway, type ImPlatform } from "../../api";
import { getLang, t } from "../../i18n";
import { toast, useStore } from "../../store";

/** 即时通讯 (in 设置 › 通知): connect Slack / Telegram / Discord / 飞书 bots. */
export function ImSetup({ onChanged }: { onChanged: () => void }) {
  const [platforms, setPlatforms] = useState<ImPlatform[]>([]);
  const [list, setList] = useState<ImGateway[] | null>(null);
  const [form, setForm] = useState<ImPlatform | null>(null);
  const [err, setErr] = useState("");

  const load = async () => {
    try {
      const r = await API.imConfig();
      // platform names in the interface language (飞书 → Feishu)
      setPlatforms(r.platforms.map((p) => (getLang() === "en" ? { ...p, name: p.nameEn || p.name, channel: p.channelEn || p.channel } : p)));
      setList(r.gateways);
    } catch (e: any) {
      setErr(e?.status === 404 ? t("dev.old") : e?.message ?? String(e));
    }
  };
  useEffect(() => {
    void load();
  }, []);

  const toggle = async (g: ImGateway) => {
    try {
      await API.saveIm(g.id, { enabled: !g.enabled });
      await load();
      onChanged();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const test = async (g: ImGateway) => {
    try {
      const r = await API.testIm(g.id);
      toast(r.ok ? t("im.testOk") : t("im.testFailed", { error: r.error ?? "" }));
    } catch (e: any) {
      toast(t("im.testFailed", { error: e?.message ?? String(e) }));
    }
  };
  const remove = async (g: ImGateway) => {
    if (!window.confirm(t("im.removeConfirm", { name: name(g.platform) }))) return;
    await API.removeIm(g.id).catch(() => {});
    await load();
    onChanged();
  };
  const name = (id: string) => platforms.find((p) => p.id === id)?.name ?? id;
  const agents = useStore((s) => s.agents);
  const teamsAll = useStore((s) => s.teams);
  const team = Object.values(agents).filter((a) => a.kind !== "chat");
  const setListen = async (g: ImGateway, agent: string, keywords?: string) => {
    try {
      await API.saveIm(g.id, { listen: { agent, keywords: keywords === undefined ? g.listen?.keywords ?? [] : [keywords] } });
      await load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };

  return (
    <>
      <div className="vsec">{t("im.title")}</div>
      {err && <p className="small errt">{err}</p>}
      {list && list.length > 0 && (
        <div className="htable">
          {list.map((g) => (
            <div key={g.id} className="hrow devrow imrow">
              <span className="hosticon sm">
                <MessageCircle size={15} />
              </span>
              <span className="g">
                <b>
                  {name(g.platform)} <span className="mono small">{g.channel}</span>
                </b>
                <span>
                  {g.error ? t("im.error", { error: g.error }) : g.running ? t("im.running") : t("im.off")} · {t("im.allowN", { n: g.allowUsers.length })}
                </span>
              </span>
              <button className="ix" type="button" title={t("im.test")} aria-label={t("im.test")} disabled={!g.running} onClick={() => void test(g)}>
                <Send size={13} />
              </button>
              <button type="button" className="tg" role="switch" aria-checked={g.enabled} aria-label={t("auto.enabled")} onClick={() => void toggle(g)} />
              <button className="ix dng" type="button" title={t("common.delete")} aria-label={t("common.delete")} onClick={() => void remove(g)}>
                <Trash2 size={13} />
              </button>
              {teamsAll.length > 1 && (
                <div className="imlisten">
                  <span className="small">{t("im.team")}</span>
                  <span className="ssel">
                    <select
                      value={g.team || ""}
                      aria-label={t("im.team")}
                      onChange={(e) =>
                        API.saveIm(g.id, { team: e.target.value }).then(
                          () => void load(),
                          (err) => toast(err?.message ?? String(err)),
                        )
                      }
                    >
                      <option value="">{t("im.teamAny")}</option>
                      {teamsAll.map((tm) => (
                        <option key={tm.id} value={tm.id}>
                          {tm.name}
                        </option>
                      ))}
                    </select>
                  </span>
                </div>
              )}
              <div className="imlisten">
                <span className="small" title={t("im.listenHelp")}>
                  {t("im.listen")}
                </span>
                <span className="ssel">
                  <select value={g.listen?.agent ?? ""} onChange={(e) => void setListen(g, e.target.value)} aria-label={t("im.listen")}>
                    <option value="">{t("im.listenOff")}</option>
                    {team.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                  </select>
                </span>
                {g.listen?.agent && (
                  <input
                    className="fi sm"
                    defaultValue={(g.listen.keywords ?? []).join(", ")}
                    placeholder={t("im.listenKeywords")}
                    onBlur={(e) => {
                      const v = e.target.value.trim();
                      if (v !== (g.listen?.keywords ?? []).join(", ")) void setListen(g, g.listen!.agent, v);
                    }}
                  />
                )}
              </div>
            </div>
          ))}
        </div>
      )}
      {!form && (
        <div className="impick">
          {platforms
            .filter((p) => !list?.some((g) => g.platform === p.id))
            .map((p) => (
              <button key={p.id} type="button" className="ap-card" onClick={() => setForm(p)}>
                <MessageCircle size={16} />
                <span>{t("im.connect", { name: p.name })}</span>
              </button>
            ))}
        </div>
      )}
      {form && (
        <ImForm
          p={form}
          onCancel={() => setForm(null)}
          onSaved={async () => {
            setForm(null);
            await load();
            onChanged();
          }}
        />
      )}
      <p className="small" style={{ marginTop: 8 }}>
        {t("im.note")}
      </p>
    </>
  );
}

function ImForm({ p, onCancel, onSaved }: { p: ImPlatform; onCancel: () => void; onSaved: () => void }) {
  const [bot, setBot] = useState("");
  const [app, setApp] = useState("");
  const [appId, setAppId] = useState("");
  const [channel, setChannel] = useState("");
  const [users, setUsers] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const save = async () => {
    setBusy(true);
    setErr("");
    try {
      await API.saveIm(p.id, { platform: p.id, botToken: bot, appToken: app || undefined, appId: appId || undefined, channel, allowUsers: [users] });
      toast(t("im.saved", { name: p.name }));
      onSaved();
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="box form imform">
      <div className="imfh">
        <b>{t("im.connect", { name: p.name })}</b>
        <a className="linkbtn" href={p.tokenDoc} target="_blank" rel="noreferrer noopener">
          {t("im.getToken")} <ExternalLink size={12} />
        </a>
        <span className="sp" />
        <button className="ix" type="button" aria-label={t("common.close")} onClick={onCancel}>
          <X size={14} />
        </button>
      </div>
      <div className="fgrid">
        <label className="fl">{t("im.bot")}</label>
        <input className="fi sm mono" type="password" autoComplete="off" value={bot} onChange={(e) => setBot(e.target.value)} />
        {p.app && (
          <>
            <label className="fl">{t("im.app")}</label>
            <input className="fi sm mono" type="password" autoComplete="off" value={app} placeholder="xapp-…" onChange={(e) => setApp(e.target.value)} />
          </>
        )}
        {p.appId && (
          <>
            <label className="fl">App ID</label>
            <input className="fi sm mono" value={appId} placeholder="cli_…" onChange={(e) => setAppId(e.target.value)} />
          </>
        )}
        <label className="fl">{t("im.channel")}</label>
        <input className="fi sm mono" value={channel} placeholder={p.channel} onChange={(e) => setChannel(e.target.value)} />
        <label className="fl">{t("im.users")}</label>
        <div>
          <input className="fi sm mono" value={users} placeholder={t("im.usersPh")} onChange={(e) => setUsers(e.target.value)} />
          <div className="small">{t("im.usersHint")}</div>
        </div>
      </div>
      <p className="small imkey">
        <CheckCircle2 size={12} /> {t("im.keyNote")}
      </p>
      {err && <div className="ferr">{err}</div>}
      <div className="acts">
        <button className="btn pri" type="button" disabled={busy || !bot || !channel || (p.app && !app) || (p.appId && !appId)} onClick={() => void save()}>
          {busy && <Loader2 size={13} className="spin" />}
          {t("im.save")}
        </button>
        <button className="btn ghost" type="button" onClick={onCancel}>
          {t("common.cancel")}
        </button>
      </div>
    </div>
  );
}
