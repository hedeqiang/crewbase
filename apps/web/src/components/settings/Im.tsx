import { Skeleton } from "../Skeleton";
import { Check, ExternalLink, KeyRound, Loader2, MessagesSquare, MoreHorizontal, Pause, Pencil, Play, Plus, Send, Trash2, X } from "lucide-react";
import { useEffect, useRef, useState, type CSSProperties } from "react";
import { API, type ImGateway, type ImPlatform, type ImRegion } from "../../api";
import { getLang, t, type Key } from "../../i18n";
import { setState, toast, useStore } from "../../store";
import { Popover, useMenu } from "../Popover";
import { Head } from "../Settings";
import slackSvg from "../../assets/im/slack.svg?raw";
import discordSvg from "../../assets/im/discord.svg?raw";
import telegramSvg from "../../assets/im/telegram.svg?raw";
import feishuSvg from "../../assets/im/feishu.svg?raw";
import { confirmDialog } from "../Confirm";

// Each platform's own mark, from its official brand assets (slack.com,
// discord.com/branding, telegram.org, feishu.cn / larksuite.com).
// Discord's symbol is white on its blurple tile.

const MARKS: Record<string, { svg: string; tile?: string; pad: number }> = {
  slack: { svg: slackSvg, pad: 0.2 },
  discord: { svg: discordSvg, tile: "#5865F2", pad: 0.22 },
  telegram: { svg: telegramSvg, pad: 0.08 },
  feishu: { svg: feishuSvg, pad: 0.14 },
};

export function ImMark({ platform, label, size = 32 }: { platform: string; label: string; size?: number }) {
  const m = MARKS[platform];
  if (!m) {
    const ch = Array.from(label.trim())[0]?.toUpperCase() ?? "?";
    return (
      <span className="pmark mono-tile immark" aria-hidden style={{ width: size, height: size, fontSize: Math.round(size * 0.46), ["--h" as string]: 210 } as CSSProperties}>
        {ch}
      </span>
    );
  }
  return (
    <span
      className="pmark immark brand"
      aria-hidden
      style={{ width: size, height: size, padding: Math.round(size * m.pad), background: m.tile } as CSSProperties}
      dangerouslySetInnerHTML={{ __html: m.svg }}
    />
  );
}

const en = () => getLang() === "en";
/** The platform's name in the interface language ("飞书 / Lark", "Feishu / Lark"). */
const pname = (p: ImPlatform) => (en() && p.nameEn) || p.name;
const rname = (r: ImRegion) => (en() && r.nameEn) || r.name;
const rbrand = (r: ImRegion) => (en() && r.brandEn) || r.brand;
const regionOf = (p: ImPlatform, id?: string) => p.regions?.find((r) => r.id === id) ?? p.regions?.[0];
/** Where to create the bot: the region's developer console, if it has regions. */
const docOf = (p: ImPlatform, region?: string) => regionOf(p, region)?.tokenDoc ?? p.tokenDoc;
const channelPh = (p: ImPlatform) => (en() && p.channelEn) || p.channel;

/** A gateway's name for people: the product (飞书 or Lark for the merged
 *  platform) — used by the notification table's columns too. */
export function imGatewayName(g: ImGateway, platforms: ImPlatform[]): string {
  const p = platforms.find((x) => x.id === g.platform);
  if (!p) return g.id;
  const r = p.regions?.length ? regionOf(p, g.region) : undefined;
  return r ? rbrand(r) : pname(p);
}

/** A gateway error in words; a token missing from the vault is the common one. */
const errText = (e = "") => (/credentials: not found/.test(e) ? t("imx.errNoToken") : e);

type Load = { platforms: ImPlatform[]; gateways: ImGateway[] };

/** 设置 › 即时通讯: one card per chat app; a connected one lists its groups.
 *  Connecting is a step dialog, not a standing form. */
export function Im() {
  const [data, setData] = useState<Load | null>(null);
  const [err, setErr] = useState("");
  const [dlg, setDlg] = useState<{ p: ImPlatform; g?: ImGateway } | null>(null);
  const [closed, setClosed] = useState<Record<string, boolean>>({});

  const load = async () => {
    try {
      setData(await API.imConfig());
      setErr("");
    } catch (e: any) {
      setErr(e?.status === 404 ? t("dev.old") : e?.message ?? String(e));
    }
  };
  useEffect(() => {
    void load();
  }, []);
  // a gateway that was just enabled takes a few seconds to reach its platform
  const pending = !!data?.gateways.some((g) => g.enabled && g.running && !g.connected && !g.error);
  useEffect(() => {
    if (!pending) return;
    const id = window.setInterval(() => void load(), 3000);
    const stop = window.setTimeout(() => window.clearInterval(id), 60_000);
    return () => {
      window.clearInterval(id);
      window.clearTimeout(stop);
    };
  }, [pending]);

  const gateways = data?.gateways ?? [];
  return (
    <>
      <Head title={t("set.im")} sub={t("imx.sub")} />
      <div className="vb">
        {err && <p className="small errt">{err}</p>}
        {data && gateways.length === 0 && (
          <div className="imintro">
            <span className="imintro-i">
              <MessagesSquare size={18} />
            </span>
            <span className="g">
              <b>{t("imx.introTitle")}</b>
              <span>{t("imx.intro")}</span>
            </span>
          </div>
        )}
        <div className="tcards">
          {!data && !err && <Skeleton />}
          {data?.platforms.map((p) => {
            const mine = gateways.filter((g) => g.platform === p.id);
            return (
              <PlatformCard
                key={p.id}
                p={p}
                list={mine}
                open={mine.length > 0 && !closed[p.id]}
                onToggle={() => setClosed({ ...closed, [p.id]: !closed[p.id] })}
                onConnect={(g) => setDlg({ p, g })}
                onChanged={load}
              />
            );
          })}
        </div>
        {data && (
          <p className="small muted tnote">
            {t("imx.foot")}
            {en() ? " " : ""}
            <button type="button" className="linkbtn" onClick={() => setState({ settings: "notify" })}>
              {t("imx.toNotify")}
            </button>
          </p>
        )}
      </div>
      {dlg && data && (
        <ConnectDialog
          p={dlg.p}
          g={dlg.g}
          taken={gateways.map((g) => g.id)}
          onClose={() => setDlg(null)}
          onSaved={async () => {
            setDlg(null);
            setClosed({ ...closed, [dlg.p.id]: false });
            await load();
          }}
        />
      )}
    </>
  );
}

/** "已连接 2 个群" / "出错：…" / "未连接" for a platform. */
function platformStatus(p: ImPlatform, list: ImGateway[]): { text: string; tone: "" | "ok" | "red" | "amber" } {
  if (!list.length) return { text: t("imx.st.none"), tone: "" };
  const bad = list.find((g) => g.enabled && g.error);
  if (bad) return { text: t("imx.st.error", { error: errText(bad.error) }), tone: "red" };
  const on = list.filter((g) => g.enabled);
  if (!on.length) return { text: t("imx.st.paused"), tone: "amber" };
  const up = on.filter((g) => g.connected).length;
  if (!up) return { text: t("imx.st.connecting"), tone: "" };
  const unit = p.id === "slack" || p.id === "discord" ? "imx.st.channels" : "imx.st.groups";
  return { text: t(unit, { n: up }), tone: "ok" };
}

function PlatformCard({
  p,
  list,
  open,
  onToggle,
  onConnect,
  onChanged,
}: {
  p: ImPlatform;
  list: ImGateway[];
  open: boolean;
  onToggle: () => void;
  onConnect: (g?: ImGateway) => void;
  onChanged: () => Promise<void>;
}) {
  const st = platformStatus(p, list);
  const connected = list.length > 0;
  const act = () => (connected ? onToggle() : onConnect());
  return (
    <div className={"tcard imcard" + (open ? " open" : "")}>
      <div className="tcardh" role="button" tabIndex={0} aria-expanded={connected ? open : undefined} onClick={act} onKeyDown={(e) => (e.key === "Enter" || e.key === " ") && e.target === e.currentTarget && (e.preventDefault(), act())}>
        <ImMark platform={p.id} label={pname(p)} />
        <span className="g">
          <b>{pname(p)}</b>
          <span className={"small imst" + (st.tone ? " " + st.tone : "")} title={st.text}>
            {st.tone && <i className={"sdot " + (st.tone === "red" ? "err" : st.tone)} />}
            {st.text}
          </span>
        </span>
        <button
          className="btn sm"
          type="button"
          aria-expanded={connected ? open : undefined}
          onClick={(e) => {
            e.stopPropagation();
            act();
          }}
        >
          {connected ? t("imx.manage") : t("imx.connect")}
        </button>
      </div>
      {open && (
        <div className="tbody">
          <div className="tlist">
            {list.map((g) => (
              <GatewayRow key={g.id} p={p} g={g} onEdit={() => onConnect(g)} onChanged={onChanged} />
            ))}
            <button className="taddrow" type="button" onClick={() => onConnect()}>
              <Plus size={14} />
              {t(p.id === "slack" || p.id === "discord" ? "imx.addChannel" : "imx.addGroup")}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function GatewayRow({ p, g, onEdit, onChanged }: { p: ImPlatform; g: ImGateway; onEdit: () => void; onChanged: () => Promise<void> }) {
  const more = useMenu();
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  const r = p.regions?.length ? regionOf(p, g.region) : undefined;
  const team = g.team ? teams.find((x) => x.id === g.team)?.name ?? g.team : "";
  const listener = g.listen?.agent ? agents[g.listen.agent]?.name ?? g.listen.agent : "";
  const dot = !g.enabled ? "idle" : g.error ? "err" : g.connected ? "ok" : "wait";
  const state = !g.enabled ? t("imx.paused") : g.error ? t("imx.st.error", { error: errText(g.error) }) : g.connected ? t("imx.live") : t("imx.st.connecting");
  const meta = [
    teams.length > 1 ? (team ? t("imx.inTeam", { team }) : t("imx.anyTeam")) : "",
    t("imx.allowN", { n: g.allowUsers.length }),
    listener && t("imx.listening", { name: listener }),
    g.relayReplies ? t("imx.relayOn") : "",
  ].filter(Boolean);

  const save = async (b: Record<string, unknown>, ok?: string) => {
    try {
      await API.saveIm(g.id, b);
      if (ok) toast(ok);
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
    await onChanged();
  };
  const test = async () => {
    more.close();
    try {
      const res = await API.testIm(g.id);
      toast(res.ok ? t("imx.testOk") : t("imx.testFailed", { error: res.error ?? "" }));
    } catch (e: any) {
      toast(t("imx.testFailed", { error: e?.message ?? String(e) }));
    }
  };
  const remove = async () => {
    more.close();
    if (!(await confirmDialog(t("imx.removeConfirm", { name: g.channel })))) return;
    await API.removeIm(g.id).catch((e) => toast(e?.message ?? String(e)));
    await onChanged();
  };
  return (
    <div className={"trow imgw" + (g.enabled ? "" : " off")}>
      <span className={"sd " + dot} title={state} aria-label={state} />
      <span className="g">
        <b>
          <span className="mono imch">{g.channel}</span>
          {r && <span className="pill sm">{rbrand(r)}</span>}
          {!g.enabled && <span className="pill sm amber">{t("imx.paused")}</span>}
        </b>
        {g.enabled && g.error ? <span className="small errt imerr">{state}</span> : <span className="small muted">{meta.join(" · ")}</span>}
      </span>
      <button className="ix" type="button" ref={more.ref} aria-label={t("rm.more")} title={t("rm.more")} onClick={more.toggle}>
        <MoreHorizontal size={15} />
      </button>
      <Popover anchor={more.anchor} open={more.open} onClose={more.close} width={200} align="end">
        <button
          className="mi"
          type="button"
          onClick={() => {
            more.close();
            onEdit();
          }}
        >
          <Pencil size={15} />
          <span className="g">{t("imx.edit")}</span>
        </button>
        <button className="mi" type="button" disabled={!g.running} onClick={() => void test()}>
          <Send size={15} />
          <span className="g">{t("imx.test")}</span>
        </button>
        <button
          className="mi"
          type="button"
          onClick={() => {
            more.close();
            void save({ enabled: !g.enabled }, g.enabled ? t("imx.pausedToast", { name: g.channel }) : undefined);
          }}
        >
          {g.enabled ? <Pause size={15} /> : <Play size={15} />}
          <span className="g">{g.enabled ? t("imx.pause") : t("imx.resume")}</span>
        </button>
        <button className="mi dng" type="button" onClick={() => void remove()}>
          <Trash2 size={15} />
          <span className="g">{t("imx.remove")}</span>
        </button>
      </Popover>
    </div>
  );
}

/** A free gateway id: the product (feishu / lark, slack…), then -2, -3. */
type Chat = { id: string; name: string; description?: string; external?: boolean };

/** 飞书 / Lark: the groups the bot is in, to pick from (needs the
 *  im:chat:readonly permission); pasting a chat_id still works. */
function GroupPicker({ query, value, onChange, placeholder }: { query: Parameters<typeof API.imChats>[0]; value: string; onChange: (v: string) => void; placeholder: string }) {
  const [list, setList] = useState<{ chats: Chat[]; hint?: string } | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [paste, setPaste] = useState(false);
  const load = async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await API.imChats(query);
      setList(r);
      // one group and nothing chosen yet: that is the one
      if (!value && r.chats.length === 1) onChange(r.chats[0].id);
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  const known = !!list?.chats.some((c) => c.id === value);
  const typing = paste || !list || list.chats.length === 0 || (!!value && !known);
  return (
    <div className="imgroups">
      {busy && !list && (
        <div className="small muted imhint">
          <Loader2 size={13} className="spin" /> {t("imx.groupsLoading")}
        </div>
      )}
      {list && list.chats.length > 0 && (
        <div className="imglist" role="radiogroup" aria-label={t("imx.group")}>
          {list.chats.map((c) => (
            <button key={c.id} type="button" role="radio" aria-checked={value === c.id} className={"imgrow" + (value === c.id ? " on" : "")} onClick={() => onChange(c.id)}>
              <span className="g">
                <b>{c.name || c.id}</b>
                <span className="small muted mono">
                  {c.id}
                  {c.external ? " · " + t("imx.groupExternal") : ""}
                </span>
              </span>
              {value === c.id && <Check size={14} />}
            </button>
          ))}
        </div>
      )}
      {(err || list?.hint) && (
        <div className={"small imhint" + (err ? " errt" : " muted")}>
          {err || t(`imx.groupsHint.${list!.hint}` as Key)}{" "}
          <button className="linkbtn" type="button" disabled={busy} onClick={() => void load()}>
            {t("imx.groupsRetry")}
          </button>
        </div>
      )}
      {typing ? (
        <>
          <input className="fi sm mono" value={value} placeholder={placeholder} onChange={(e) => onChange(e.target.value)} aria-label={t("imx.group")} />
          <div className="small muted imhint">{t("imx.chHint.feishu")}</div>
        </>
      ) : (
        <button className="linkbtn small" type="button" onClick={() => setPaste(true)}>
          {t("imx.groupPaste")}
        </button>
      )}
    </div>
  );
}

function newId(p: ImPlatform, region: string | undefined, taken: string[]) {
  const base = p.id === "feishu" && region === "intl" ? "lark" : p.id;
  if (!taken.includes(base)) return base;
  for (let i = 2; ; i++) if (!taken.includes(`${base}-${i}`)) return `${base}-${i}`;
}

const STEPS = ["imx.step1", "imx.step2", "imx.step3"] as const;

function ConnectDialog({ p, g, taken, onClose, onSaved }: { p: ImPlatform; g?: ImGateway; taken: string[]; onClose: () => void; onSaved: () => void }) {
  const editing = !!g;
  const agents = useStore((s) => s.agents);
  const teams = useStore((s) => s.teams);
  const team = Object.values(agents).filter((a) => a.kind !== "chat");
  const [step, setStep] = useState(editing ? 2 : 0);
  const [region, setRegion] = useState(g?.region ?? p.regions?.[0]?.id);
  const [appId, setAppId] = useState(g?.appId ?? "");
  const [bot, setBot] = useState("");
  const [app, setApp] = useState("");
  const [check, setCheck] = useState<{ busy?: boolean; ok?: boolean; msg?: string } | null>(null);
  const [channel, setChannel] = useState(g?.channel ?? "");
  const [users, setUsers] = useState((g?.allowUsers ?? []).join(", "));
  const [teamId, setTeamId] = useState(g?.team ?? "");
  const [listen, setListen] = useState(g?.listen?.agent ?? "");
  const [keywords, setKeywords] = useState((g?.listen?.keywords ?? []).join(", "));
  const [relay, setRelay] = useState(g?.relayReplies ?? true);
  const [approvals, setApprovals] = useState(g?.approvals ?? true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const first = useRef<HTMLInputElement>(null);
  useEffect(() => first.current?.focus(), [step]);
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);

  const r = regionOf(p, region);
  const brand = r ? rbrand(r) : pname(p);
  const credsOk = (!!bot.trim() || !!g?.hasToken) && (!p.app || !!app.trim() || !!g?.hasAppToken) && (!p.appId || !!appId.trim());
  const regionChanged = editing && !!p.regions?.length && region !== g?.region;

  const verify = async () => {
    setCheck({ busy: true });
    try {
      const res = await API.verifyIm({ platform: p.id, region, appId: appId.trim() || undefined, botToken: bot.trim() || undefined, appToken: app.trim() || undefined, id: g?.id });
      setCheck(res.ok ? { ok: true, msg: res.bot ? t("imx.checkOkAs", { bot: res.bot }) : t("imx.checkOk") } : { ok: false, msg: res.error ?? "" });
    } catch (e: any) {
      setCheck({ ok: false, msg: e?.message ?? String(e) });
    }
  };
  const save = async () => {
    setBusy(true);
    setErr("");
    try {
      const id = g?.id ?? newId(p, region, taken);
      await API.saveIm(id, {
        platform: p.id,
        region: p.regions?.length ? region : undefined,
        appId: p.appId ? appId.trim() || undefined : undefined,
        botToken: bot.trim() || undefined,
        appToken: (p.app && app.trim()) || undefined,
        channel: channel.trim(),
        allowUsers: [users],
        team: teamId,
        listen: { ...(g?.listen ?? {}), agent: listen, keywords: listen ? [keywords] : [] },
        relayReplies: relay,
        approvals,
        ...(editing ? {} : { enabled: true }),
      });
      toast(editing ? t("imx.savedEdit") : t("imx.saved", { name: brand }));
      onSaved();
    } catch (e: any) {
      setErr(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const credChange = (set: (v: string) => void) => (e: { target: { value: string } }) => {
    set(e.target.value);
    setCheck(null);
  };

  const regionSeg = !!p.regions?.length && step < 2 && (
    <div className="imregion">
      <span className="small muted">{t("imx.region")}</span>
      <div className="seg sm" role="radiogroup" aria-label={t("imx.region")}>
        {p.regions!.map((x) => (
          <button
            key={x.id}
            type="button"
            role="radio"
            aria-checked={region === x.id}
            aria-pressed={region === x.id}
            onClick={() => {
              setRegion(x.id);
              setCheck(null);
            }}
          >
            {rname(x)} · {rbrand(x)}
          </button>
        ))}
      </div>
    </div>
  );

  const canNext = step === 0 || (step === 1 && credsOk);
  const stepsKey = `imx.how.${p.id}` as Key;
  return (
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal imdlg" role="dialog" aria-modal="true" aria-labelledby="imTitle">
        <div className="mhd">
          <ImMark platform={p.id} label={brand} size={24} />
          <h3 id="imTitle">{editing ? t("imx.editTitle", { name: brand }) : t("imx.connectTitle", { name: brand })}</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={15} />
          </button>
        </div>
        <div className="mbd">
          <ol className="imsteps">
            {STEPS.map((k, i) => (
              <li key={k} className={i === step ? "on" : i < step ? "done" : ""}>
                <button type="button" disabled={i > step && !(i === 2 && credsOk && step === 1)} onClick={() => setStep(i)} aria-current={i === step ? "step" : undefined}>
                  <span className="wnum">{i < step ? <Check size={12} /> : i + 1}</span>
                  <span className="imstep-l">{t(k)}</span>
                </button>
              </li>
            ))}
          </ol>
          {regionSeg}

          {step === 0 && (
            <div className="impane">
              <ol className="imhow">
                {t(stepsKey)
                  .split("\n")
                  .map((line, i) => (
                    <li key={i}>{line}</li>
                  ))}
              </ol>
              <a className="btn sm imdoc" href={docOf(p, region)} target="_blank" rel="noreferrer noopener">
                {t("imx.openDoc", { site: new URL(docOf(p, region)).host })} <ExternalLink size={12} />
              </a>
            </div>
          )}

          {step === 1 && (
            <form
              className="impane"
              onSubmit={(e) => {
                e.preventDefault();
                if (credsOk) setStep(2);
              }}
            >
              {p.appId && (
                <>
                  <label className="fl" htmlFor="im-appid">
                    App ID
                  </label>
                  <input id="im-appid" ref={first} className="fi sm mono" value={appId} placeholder="cli_…" autoComplete="off" onChange={credChange(setAppId)} />
                </>
              )}
              <label className="fl" htmlFor="im-bot">
                {p.appId ? "App Secret" : t("imx.botToken")}
              </label>
              <input
                id="im-bot"
                ref={p.appId ? undefined : first}
                className="fi sm mono"
                type="password"
                autoComplete="off"
                value={bot}
                placeholder={editing && g?.hasToken ? t("imx.keepSaved") : p.id === "slack" ? "xoxb-…" : ""}
                onChange={credChange(setBot)}
              />
              {p.app && (
                <>
                  <label className="fl" htmlFor="im-app">
                    {t("imx.appToken")}
                  </label>
                  <input
                    id="im-app"
                    className="fi sm mono"
                    type="password"
                    autoComplete="off"
                    value={app}
                    placeholder={editing && g?.hasAppToken ? t("imx.keepSaved") : "xapp-…"}
                    onChange={credChange(setApp)}
                  />
                </>
              )}
              <div className="imcheck">
                <button className="btn sm" type="button" disabled={check?.busy || !credsOk} onClick={() => void verify()}>
                  {check?.busy ? <Loader2 size={13} className="spin" /> : <KeyRound size={13} />}
                  {t("imx.check")}
                </button>
                {check && !check.busy && (
                  <span className={"small " + (check.ok ? "okt" : "errt")} role="status">
                    {check.ok ? <Check size={12} /> : <X size={12} />} {check.msg}
                  </span>
                )}
              </div>
              {regionChanged && <p className="small muted">{t("imx.regionChanged")}</p>}
              <p className="small muted imkeynote">{t("imx.keyNote")}</p>
              <button type="submit" hidden />
            </form>
          )}

          {step === 2 && (
            <form
              className="impane"
              onSubmit={(e) => {
                e.preventDefault();
                if (channel.trim()) void save();
              }}
            >
              <label className="fl" htmlFor="im-ch">
                {t(p.id === "slack" || p.id === "discord" ? "imx.channel" : "imx.group")}
              </label>
              {p.id === "feishu" ? (
                <GroupPicker
                  query={{ platform: p.id, region, appId: appId.trim() || undefined, botToken: bot.trim() || undefined, id: g?.id }}
                  value={channel}
                  onChange={setChannel}
                  placeholder={channelPh(p)}
                />
              ) : (
                <>
                  <input id="im-ch" ref={first} className="fi sm mono" value={channel} placeholder={channelPh(p)} onChange={(e) => setChannel(e.target.value)} />
                  <div className="small muted imhint">{t(`imx.chHint.${p.id}` as Key)}</div>
                </>
              )}

              <label className="fl" htmlFor="im-users">
                {t("imx.users")}
              </label>
              <input id="im-users" className="fi sm mono" value={users} placeholder={t("imx.usersPh")} onChange={(e) => setUsers(e.target.value)} />
              <div className="small muted imhint">{t("imx.usersHint")}</div>

              <div className="imtwo">
                {teams.length > 1 && (
                  <div>
                    <label className="fl" htmlFor="im-team">
                      {t("imx.team")}
                    </label>
                    <span className="ssel">
                      <select id="im-team" value={teamId} onChange={(e) => setTeamId(e.target.value)}>
                        <option value="">{t("imx.teamAny")}</option>
                        {teams.map((tm) => (
                          <option key={tm.id} value={tm.id}>
                            {tm.name}
                          </option>
                        ))}
                      </select>
                    </span>
                    <div className="small muted imhint">{t("imx.teamHint")}</div>
                  </div>
                )}
                <div>
                  <label className="fl" htmlFor="im-listen" title={t("imx.listenHelp")}>
                    {t("imx.listen")}
                  </label>
                  <span className="ssel">
                    <select id="im-listen" value={listen} onChange={(e) => setListen(e.target.value)}>
                      <option value="">{t("imx.listenOff")}</option>
                      {team.map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.name}
                        </option>
                      ))}
                    </select>
                  </span>
                </div>
              </div>
              {listen ? (
                <>
                  <input className="fi sm imkw" value={keywords} placeholder={t("imx.listenKeywords")} aria-label={t("imx.listenKeywords")} onChange={(e) => setKeywords(e.target.value)} />
                  <div className="small muted imhint">{t("imx.listenHelp")}</div>
                </>
              ) : null}

              <div className="imtoggles">
                <label className="imtg">
                  <span className="g">
                    <b>{t("imx.relay")}</b>
                    <span>{t("imx.relayDesc")}</span>
                  </span>
                  <button type="button" className="tg" role="switch" aria-checked={relay} aria-label={t("imx.relay")} onClick={() => setRelay(!relay)} />
                </label>
                <label className="imtg">
                  <span className="g">
                    <b>{t("imx.approvals")}</b>
                    <span>{t("imx.approvalsDesc")}</span>
                  </span>
                  <button type="button" className="tg" role="switch" aria-checked={approvals} aria-label={t("imx.approvals")} onClick={() => setApprovals(!approvals)} />
                </label>
              </div>
              {err && <div className="ferr">{err}</div>}
              <button type="submit" hidden />
            </form>
          )}

          <div className="mft">
            {step > 0 && (
              <button className="btn" type="button" onClick={() => setStep(step - 1)}>
                {t("imx.back")}
              </button>
            )}
            <span className="sp" />
            <button className="btn" type="button" onClick={onClose}>
              {t("common.cancel")}
            </button>
            {step < 2 ? (
              <button className="btn pri" type="button" disabled={!canNext} onClick={() => setStep(step + 1)}>
                {step === 0 ? t("imx.haveBot") : t("imx.next")}
              </button>
            ) : (
              <button className="btn pri" type="button" disabled={busy || !channel.trim()} onClick={() => void save()}>
                {busy && <Loader2 size={13} className="spin" />}
                {editing ? t("common.save") : t("imx.save")}
              </button>
            )}
          </div>
        </div>
      </div>
    </>
  );
}
