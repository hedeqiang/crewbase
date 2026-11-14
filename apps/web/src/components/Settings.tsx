import { useEffect, useMemo, useState, type ReactNode } from "react";
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
import { API, type ApprovalHistoryItem, type ProviderView, type RememberedChoice, type Settings as SettingsData } from "../api";
import { t, type Key, type Lang } from "../i18n";
import { ago, kfmt, modeLabel, providerName, until } from "../format";
import { openSession, setLang, setState, toast, useStore, type SettingsSection } from "../store";
import { Automation } from "./settings/Automation";
import { Extensions } from "./settings/Extensions";
import { Profile } from "./settings/Profile";
import { Routing } from "./settings/Routing";
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
        {sec === "auto" && <Automation />}
        {sec === "market" && <Extensions />}
        {sec === "profile" && <Profile />}
        {!["general", "models", "approvals", "auto", "market", "profile"].includes(sec) && <Soon sec={sec} />}
      </div>
    </div>
  );
}

export function Head({ title, sub, right }: { title: string; sub?: string; right?: ReactNode }) {
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

export function Row({ title, desc, children, dim }: { title: string; desc?: string; children: ReactNode; dim?: boolean }) {
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
        <Defaults />
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

export function useSettingsData() {
  const [st, setSt] = useState<SettingsData | null>(null);
  useEffect(() => {
    API.settings().then(setSt, () => {});
  }, []);
  const patch = async (b: Partial<SettingsData>) => {
    try {
      const r = await API.patchSettings(b);
      setSt(r);
      toast(t("panel.saved"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return [st, patch] as const;
}

function Defaults() {
  const [st, patch] = useSettingsData();
  const cur = st?.defaultMode || "sandbox";
  return (
    <div className="sbox">
      <Row title={t("set.defaultMode")} desc={t("set.defaultModeDesc")}>
        <div className="seg">
          {(["read-only", "sandbox"] as const).map((m) => (
            <button key={m} type="button" aria-pressed={cur === m} disabled={!st} onClick={() => patch({ defaultMode: m })}>
              {modeLabel(m)}
            </button>
          ))}
        </div>
      </Row>
      <Row title={t("set.defaultModels")} desc={t("set.defaultModelsDesc", { n: st?.defaultModels.length ?? 0 })}>
        <button className="btn" type="button" onClick={() => setState({ settings: "models", modelsTab: "routing" })}>
          {t("set.routing")}
        </button>
      </Row>
    </div>
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
  const tab = useStore((s) => s.modelsTab);
  const setTab = (v: "providers" | "routing") => setState({ modelsTab: v });
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
        <Routing />
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
              {a ? <Avatar agent={a} size={36} /> : <span className="wfi big"><Workflow size={17} /></span>}
              <div style={{ minWidth: 0 }}>
                <div className="small" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                  <span className="pill amber">{t("appr.waiting")}</span>
                  {a?.name ?? (r.agent || t("appr.workflow"))} · 「{r.reason || r.category}」 · {ago(r.asked)}
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
        <Remembered />
        <History />
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

function Remembered() {
  const agents = useStore((s) => s.agents);
  const n = useStore((s) => s.approvals.length);
  const [list, setList] = useState<RememberedChoice[] | null>(null);
  const load = () => API.remembered().then((r) => setList(r.remembered ?? []), () => setList([]));
  useEffect(() => {
    void load();
  }, [n]);
  const revoke = async (c: RememberedChoice) => {
    const name = agents[c.agent]?.name ?? c.agent;
    if (!window.confirm(t("set.revokeConfirm", { agent: name, category: c.category }))) return;
    try {
      await API.forget(c.agent, c.category);
      toast(t("set.revoked", { agent: name, category: c.category }));
      void load();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <>
      <div className="vsec">
        {t("set.remembered")} {list && list.length > 0 ? `· ${list.length}` : ""}
      </div>
      {list === null ? (
        <p className="small">{t("common.loading")}</p>
      ) : list.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("set.rememberedNone")}
        </p>
      ) : (
        <div className="sbox">
          {list.map((c) => {
            const a = agents[c.agent];
            return (
              <div className="srow" key={c.agent + "/" + c.category}>
                <Avatar agent={a} size={28} />
                <div className="g">
                  <b>{t("set.rememberedRow", { agent: a?.name ?? c.agent, category: catLabel(c.category) })}</b>
                  <span>
                    {t("set.rememberedAt", { time: ago(c.at) })}
                    {c.overridden ? ` · ${t("set.overridden")}` : ""}
                  </span>
                </div>
                <div className="c">
                  <button className="btn" type="button" onClick={() => revoke(c)}>
                    {t("set.revoke")}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </>
  );
}

export function catLabel(c: string): string {
  const k = `cat.${c}` as Key;
  const v = t(k);
  return v === k ? c : v;
}

function History() {
  const agents = useStore((s) => s.agents);
  const n = useStore((s) => s.approvals.length);
  const [list, setList] = useState<ApprovalHistoryItem[] | null>(null);
  useEffect(() => {
    API.approvalHistory(60).then((r) => setList((r.history ?? []).filter((h) => h.decision)), () => setList([]));
  }, [n]);
  return (
    <>
      <div className="vsec">{t("set.history")}</div>
      {list === null ? (
        <p className="small">{t("common.loading")}</p>
      ) : list.length === 0 ? (
        <p className="small" style={{ margin: 0 }}>
          {t("set.historyNone")}
        </p>
      ) : (
        <div className="htable">
          {list.map((h) => {
            const a = agents[h.agent];
            const o = h.decision?.outcome ?? "";
            const tone = o === "allowed-once" ? "ok" : o === "rejected" ? "red" : "";
            return (
              <button className="hrow" type="button" key={h.callId} onClick={() => openSession(h.session)}>
                <Avatar agent={a} size={22} />
                <span className="g">
                  <b>{h.summary || h.tool}</b>
                  <span>
                    {a?.name ?? h.agent} · {catLabel(h.category || h.tool)}
                    {h.decision?.message ? ` · 「${h.decision.message}」` : ""}
                  </span>
                </span>
                <span className={"pill " + tone}>
                  {t(o === "allowed-once" ? (h.decision?.remember ? "set.hAllowedRemember" : "set.hAllowed") : o === "rejected" ? "set.hRejected" : o === "cancelled" ? "set.hCancelled" : "set.hOther")}
                </span>
                <span className="small tm">{ago(h.decided || h.asked)}</span>
              </button>
            );
          })}
        </div>
      )}
    </>
  );
}
