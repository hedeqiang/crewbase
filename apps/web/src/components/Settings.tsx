import { useMemo, useState, type ReactNode } from "react";
import {
  Bell,
  ChevronLeft,
  Cpu,
  Info,
  Keyboard,
  ListChecks,
  MousePointer2,
  Plus,
  Puzzle,
  Search,
  Server,
  Settings as Gear,
  User,
  Workflow,
} from "lucide-react";
import type { ProviderView } from "../api";
import { t, type Key, type Lang } from "../i18n";
import { ago, kfmt, providerName, until } from "../format";
import { openSession, setLang, setState, useStore, type SettingsSection } from "../store";
import { Avatar } from "./Avatar";
import { ApprovalActions } from "./Approval";
import { ProviderMark } from "./ProviderMark";
import { ThemeSeg } from "./SessionList";

const NAV: { group: Key; items: { id: SettingsSection; icon: ReactNode }[] }[] = [
  {
    group: "set.group.you",
    items: [
      { id: "general", icon: <Gear size={16} /> },
      { id: "profile", icon: <User size={16} /> },
      { id: "notify", icon: <Bell size={16} /> },
      { id: "keys", icon: <Keyboard size={16} /> },
    ],
  },
  {
    group: "set.group.agent",
    items: [
      { id: "models", icon: <Cpu size={16} /> },
      { id: "market", icon: <Puzzle size={16} /> },
      { id: "computer", icon: <MousePointer2 size={16} /> },
      { id: "auto", icon: <Workflow size={16} /> },
      { id: "approvals", icon: <ListChecks size={16} /> },
    ],
  },
  { group: "set.group.device", items: [{ id: "hosts", icon: <Server size={16} /> }] },
];

export const SECTION_ICONS: Record<SettingsSection, ReactNode> = Object.fromEntries(NAV.flatMap((g) => g.items.map((i) => [i.id, i.icon]))) as any;
export const SECTIONS: SettingsSection[] = NAV.flatMap((g) => g.items.map((i) => i.id));

export function Settings() {
  const sec = useStore((s) => s.settings);
  const n = useStore((s) => s.approvals.length);
  const [q, setQ] = useState("");
  const ql = q.trim().toLowerCase();
  return (
    <div className="setwrap">
      <nav className="snav" aria-label={t("tb.settings")}>
        <button className="back" type="button" onClick={() => setState({ view: "chat" })}>
          <ChevronLeft size={16} />
          {t("set.back")}
        </button>
        <label className="search sset">
          <Search size={14} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("set.search")} />
        </label>
        {NAV.map((g) => {
          const items = g.items.filter((i) => !ql || t(`set.${i.id}` as Key).toLowerCase().includes(ql));
          if (!items.length) return null;
          return (
            <div key={g.group} className="sgrp">
              <div className="sh">{t(g.group)}</div>
              {items.map((i) => (
                <button key={i.id} type="button" aria-current={sec === i.id} onClick={() => setState({ settings: i.id })}>
                  {i.icon}
                  {t(`set.${i.id}` as Key)}
                  {i.id === "approvals" && n > 0 && <span className="sn">{n}</span>}
                </button>
              ))}
            </div>
          );
        })}
      </nav>
      <div className="vw">
        {sec === "general" && <General />}
        {sec === "models" && <Models />}
        {sec === "approvals" && <Approvals />}
        {!["general", "models", "approvals"].includes(sec) && <Soon sec={sec} />}
      </div>
    </div>
  );
}

function Head({ title, sub, right }: { title: string; sub?: string; right?: ReactNode }) {
  return (
    <div className="vh">
      <h2>{title}</h2>
      {sub && <span className="sub">{sub}</span>}
      {right && <div className="r">{right}</div>}
    </div>
  );
}

function Soon({ sec }: { sec: SettingsSection }) {
  return (
    <>
      <Head title={t(`set.${sec}` as Key)} />
      <div className="vb">
        <div className="soon">
          <div className="chico">{SECTION_ICONS[sec]}</div>
          <b>{t("common.soon")}</b>
          <span className="small">{t("common.soonDesc")}</span>
        </div>
      </div>
    </>
  );
}

function Row({ title, desc, children, dim }: { title: string; desc?: string; children: ReactNode; dim?: boolean }) {
  return (
    <div className={"srow" + (dim ? " dim" : "")}>
      <div className="g">
        <b>{title}</b>
        {desc && <span>{desc}</span>}
      </div>
      <div className="c">{children}</div>
    </div>
  );
}

function General() {
  const theme = useStore((s) => s.theme);
  const lang = useStore((s) => s.lang);
  const connected = useStore((s) => s.connected);
  return (
    <>
      <Head title={t("set.general")} />
      <div className="vb">
        <div className="vsec">{t("set.appearance")}</div>
        <div className="sbox">
          <Row title={t("set.theme")} desc={t("set.themeDesc")}>
            <ThemeSeg theme={theme} />
          </Row>
          <Row title={t("set.language")} desc={t("set.languageDesc")}>
            <span className="ssel">
              <select value={lang} onChange={(e) => setLang(e.target.value as Lang)}>
                <option value="zh">简体中文</option>
                <option value="en">English</option>
              </select>
            </span>
          </Row>
        </div>
        <div className="vsec">{t("set.startup")}</div>
        <div className="sbox">
          <Row title={t("set.keepRunning")} desc={t("set.keepRunningDesc")}>
            <span className="tg" role="switch" aria-checked="true" aria-disabled="true" />
          </Row>
        </div>
        <div className="vsec">{t("set.defaults")}</div>
        <div className="sbox">
          <Row title={t("set.defaultMode")} desc={t("set.defaultModeDesc")} dim>
            <span className="pill">{t("mode.sandbox")}</span>
            <span className="small">{t("common.soon")}</span>
          </Row>
        </div>
        <div className="vsec">{t("set.connection")}</div>
        <div className="sbox">
          <Row title={connected ? t("set.connected") : t("set.disconnected")} desc={window.location.host}>
            <i className={"hdot" + (connected ? " on" : "")} />
          </Row>
        </div>
      </div>
    </>
  );
}

function providerState(p: ProviderView, exhausted: boolean): "ok" | "err" | "" {
  if (p.error || exhausted) return "err";
  return "ok";
}

function Models() {
  const models = useStore((s) => s.models);
  const agents = useStore((s) => s.agents);
  const [sel, setSel] = useState<string | null>(null);
  const [tab, setTab] = useState<"providers" | "routing">("providers");
  const [q, setQ] = useState("");
  const providers = models?.providers ?? [];
  const exhaustedOf = (p: ProviderView) =>
    (models?.exhausted ?? []).filter((x) => x.model === p.id || x.model.startsWith(p.id + "/"));
  const isEx = (p: ProviderView) => exhaustedOf(p).length > 0 || (!!p.quota?.known && p.quota.remaining <= 0);
  const groups = useMemo(() => {
    const ql = q.trim().toLowerCase();
    const out: [string, ProviderView[]][] = [];
    for (const kind of ["subscription", "api", "local"]) {
      const ps = providers.filter((p) => (p.kind === kind || (kind === "api" && !["subscription", "local"].includes(p.kind))) && (!ql || p.id.toLowerCase().includes(ql) || providerName(p.id).toLowerCase().includes(ql)));
      if (ps.length) out.push([kind, ps]);
    }
    return out;
  }, [providers, q]);
  const cur = providers.find((p) => p.id === sel) ?? groups[0]?.[1][0];
  const users = cur ? Object.values(agents).filter((a) => (a.models ?? []).some((r) => r.provider === cur.id)) : [];

  return (
    <>
      <Head
        title={t("set.models")}
        right={
          <>
            <div className="seg">
              <button type="button" aria-pressed={tab === "providers"} onClick={() => setTab("providers")}>
                {t("set.providers")}
              </button>
              <button type="button" aria-pressed={tab === "routing"} onClick={() => setTab("routing")}>
                {t("set.routing")}
              </button>
            </div>
            <button className="btn" type="button" disabled title={t("common.soon")}>
              <Plus size={14} />
              {t("set.addProvider")}
            </button>
          </>
        }
      />
      {tab === "routing" ? (
        <div className="vb">
          <div className="box rulenote">
            <Info size={16} />
            <div>
              <b>{t("common.soon")}</b>
              <span>{t("set.routingSoon")}</span>
            </div>
          </div>
          {models?.switches !== undefined && <p className="small">{t("set.switches", { n: models.switches })}</p>}
        </div>
      ) : providers.length === 0 ? (
        <div className="vb">
          <div className="empty">{models ? t("set.noProviders") : t("common.loading")}</div>
        </div>
      ) : (
        <div className="pvwrap">
          <div className="pvlist">
            <label className="search">
              <Search size={14} />
              <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("set.searchProviders")} />
            </label>
            {groups.map(([kind, ps]) => (
              <div key={kind}>
                <div className="pvg">{t(`kind.${kind}` as Key)}</div>
                {ps.map((p) => {
                  const st = providerState(p, isEx(p));
                  return (
                    <button key={p.id} type="button" className="pvi" aria-current={cur?.id === p.id} onClick={() => setSel(p.id)}>
                      <ProviderMark provider={p.id} size={22} />
                      <span className="g">
                        <b>{providerName(p.id)}</b>
                        <span>{p.error ? t("set.errorState") : isEx(p) ? t("set.exhaustedState") : t("set.connectedState")}</span>
                      </span>
                      {st && <span className={"sdot " + st} />}
                    </button>
                  );
                })}
              </div>
            ))}
          </div>
          {cur && (
            <div className="pvd">
              <div className="pvh">
                <ProviderMark provider={cur.id} size={36} />
                <div className="g">
                  <b>{providerName(cur.id)}</b>
                  <span>
                    {cur.id} · {t(`kind.${cur.kind}` as Key)}
                  </span>
                </div>
                <span className={"pill " + (cur.error ? "red" : isEx(cur) ? "red" : "ok")}>
                  {cur.error ? t("set.errorState") : isEx(cur) ? t("set.exhaustedState") : t("set.connectedState")}
                </span>
              </div>
              <div className="pvusers">
                {users.length ? (
                  <>
                    {t("set.inUse")}
                    {users.map((a) => (
                      <button key={a.id} className="uchip" type="button" onClick={() => openSession(a.session)}>
                        <Avatar agent={a} size={18} />
                        {a.name}
                      </button>
                    ))}
                  </>
                ) : (
                  <span className="small">{t("set.inUseNone")}</span>
                )}
              </div>
              {cur.error && <div className="confirmbar errbar">{cur.error}</div>}
              {exhaustedOf(cur).map((x) => (
                <div key={x.model} className="confirmbar errbar">
                  {t("set.exhaustedUntil", { model: x.model, time: until(x.until) })}
                </div>
              ))}
              <div className="psec2">{t("set.quota")}</div>
              <div className="box">
                {cur.quota?.known ? (
                  <QuotaRow label={t("set.quota")} remaining={cur.quota.remaining} resetsAt={cur.quota.resetsAt} />
                ) : (
                  <span className="small">{t("set.quotaUnknown")}</span>
                )}
              </div>
              <div className="psec2">{t("set.availableModels", { n: cur.models.length })}</div>
              {cur.models.length === 0 ? (
                <div className="empty">{t("set.noModels")}</div>
              ) : (
                <div className="mtable">
                  {cur.models.map((m) => (
                    <div className="mrow" key={m.id}>
                      <ProviderMark provider={cur.id} size={18} />
                      <span className="g">
                        <b>{m.name || m.id}</b>
                        <span className="mono">{m.id}</span>
                      </span>
                      <span className="caps">
                        {(m.capabilities ?? []).map((c) => (
                          <span key={c} className={"cap c-" + c.split("-")[0]}>
                            {t(`set.cap.${c}` as Key) === `set.cap.${c}` ? c : t(`set.cap.${c}` as Key)}
                          </span>
                        ))}
                      </span>
                      <span className="mctx">{m.contextWindow ? kfmt(m.contextWindow) : "—"}</span>
                      <span className={"tg" + (m.enabled ? "" : "")} role="switch" aria-checked={m.enabled} aria-disabled="true" title={t("common.soon")} />
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </>
  );
}

function QuotaRow({ label, remaining, resetsAt }: { label: string; remaining: number; resetsAt?: string }) {
  const pct = Math.round(Math.max(0, Math.min(1, remaining)) * 100);
  const out = pct <= 0;
  return (
    <div className="qrow">
      <span>{label}</span>
      <div className="meter">
        <i className={out ? "red" : pct <= 10 ? "amber" : ""} style={{ width: Math.max(out ? 100 : pct, 2) + "%" }} />
      </div>
      <span className={out ? "errt" : ""}>
        {out ? t("set.exhaustedState") : t("set.quotaLeft", { pct })}
        {resetsAt && !resetsAt.startsWith("0001") ? ` · ${t("set.quotaResets", { time: until(resetsAt) })}` : ""}
      </span>
    </div>
  );
}

function Approvals() {
  const approvals = useStore((s) => s.approvals);
  const agents = useStore((s) => s.agents);
  return (
    <>
      <Head title={t("set.approvals")} />
      <div className="vb">
        <div className="vsec">{t("set.pending", { n: approvals.length })}</div>
        {approvals.length === 0 && <p className="small">{t("appr.none")}</p>}
        {approvals.map((r) => {
          const a = agents[r.agent];
          return (
            <div className="ap" key={r.callId}>
              <Avatar agent={a} size={36} />
              <div style={{ minWidth: 0 }}>
                <div className="small" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                  <span className="pill amber">{t("appr.waiting")}</span>
                  {a?.name ?? r.agent} · 「{r.reason || r.category}」 · {ago(r.asked)}
                </div>
                <h4>{r.summary || r.tool}</h4>
                <div className="small mono">
                  {r.tool}
                  {r.category ? ` · ${r.category}` : ""}
                </div>
              </div>
              <ApprovalActions
                req={r}
                extra={
                  <button className="btn ghost" type="button" onClick={() => openSession(r.session)}>
                    {t("appr.open")}
                  </button>
                }
              />
            </div>
          );
        })}
        <div className="vsec">{t("set.remembered")}</div>
        <p className="small" style={{ margin: 0 }}>
          {t("set.rememberedSoon")}
        </p>
        <div className="box rulenote">
          <Info size={16} />
          <div>
            <b>{t("set.whichAsk")}</b>
            <span>{t("set.whichAskDesc")}</span>
          </div>
        </div>
      </div>
    </>
  );
}
