// 设置 › 模型: providers and their models (on / off, prices, quota),
// adding and editing a provider, routing and the model catalog.

import { AddProviderDialog } from "./AddProvider";
import { MagpieModelsDialog, ModelCatalog } from "./ModelSources";
import { useEffect, useMemo, useState } from "react";
import { Globe, KeyRound, ListChecks, Pencil, Plus, Search, Trash2 } from "lucide-react";
import { Head } from "./Head";
import { API, type ModelInfo, type ProviderView, type UserProvider } from "../../api";
import { t, type Key } from "../../i18n";
import { kfmt, modelKind, priceLong, priceShort, providerName, until } from "../../format";
import { openSession, refreshModels, setState, toast, useStore, resync } from "../../store";
import { Routing } from "./Routing";
import { Avatar } from "../Avatar";
import { ProviderMark } from "../ProviderMark";
import { QuotaAccounts } from "./QuotaAccounts";
import { confirmDialog } from "../Confirm";

function providerState(p: ProviderView, exhausted: boolean): "ok" | "err" | "" {
  if (p.error || exhausted) return "err";
  return "ok";
}

export function Models() {
  const models = useStore((s) => s.models);
  const agents = useStore((s) => s.agents);
  const [sel, setSel] = useState<string | null>(null);
  const tab = useStore((s) => s.modelsTab);
  const setTab = (v: "providers" | "routing" | "catalog") => setState({ modelsTab: v });
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<UserProvider | null>(null);
  const [choosingMagpie, setChoosingMagpie] = useState(false);
  // 额度 changes while you look (and was read when the app started, maybe
  // before a gateway had it): fresh on opening, then every minute
  useEffect(() => {
    void refreshModels();
    const iv = window.setInterval(() => document.visibilityState === "visible" && void refreshModels(), 60_000);
    return () => window.clearInterval(iv);
  }, []);
  const [userProv, setUserProv] = useState<UserProvider[]>([]);
  const pname = (id: string) => userProv.find((u) => u.id === id)?.name || providerName(id);
  const loadUser = () =>
    API.providers()
      .then((r) => setUserProv(r.providers))
      .catch(() => setUserProv([]));
  useEffect(() => {
    void loadUser();
  }, [models]);
  const providers = useMemo(() => models?.providers ?? [], [models]);
  const exhaustedOf = (p: ProviderView) =>
    (models?.exhausted ?? []).filter((x) => x.model === p.id || x.model.startsWith(p.id + "/"));
  const isEx = (p: ProviderView) => exhaustedOf(p).length > 0 || (!!p.quota?.known && p.quota.remaining <= 0);
  const groups = useMemo(() => {
    const ql = q.trim().toLowerCase();
    const out: [string, ProviderView[]][] = [];
    for (const kind of ["api", "local"]) {
      const ps = providers.filter((p) => (p.kind === kind || (kind === "api" && p.kind !== "local")) && (!ql || p.id.toLowerCase().includes(ql) || pname(p.id).toLowerCase().includes(ql)));
      if (ps.length) out.push([kind, ps]);
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providers, q, userProv]);
  const cur = providers.find((p) => p.id === sel) ?? groups[0]?.[1][0];
  // every model of the selected provider with its on/off switch (the
  // pickers' list leaves out what magpie / added providers have off)
  const [sw, setSw] = useState<{ provider: string; models: ModelInfo[] } | null>(null);
  const [swBusy, setSwBusy] = useState<string | null>(null);
  // the full list with switches loads only when the person asks to edit
  const [editingList, setEditingList] = useState(false);
  useEffect(() => setEditingList(false), [cur?.id]);
  useEffect(() => {
    if (!cur || !editingList) return;
    let live = true;
    API.modelSwitches(cur.id).then(
      (r) => live && setSw({ provider: cur.id, models: r.models ?? [] }),
      () => live && setSw(null),
    );
    return () => {
      live = false;
    };
  }, [cur?.id, cur?.models, editingList]);
  // by default only the models in use (enabled); 编辑 shows every model the
  // provider offers, with its switch
  const listed = cur
    ? editingList && sw?.provider === cur.id && sw.models.length
      ? sw.models
      : cur.models.filter((m) => m.enabled !== false)
    : [];
  const flip = async (m: ModelInfo) => {
    if (!cur) return;
    setSwBusy(m.id);
    try {
      const r = await API.setModelSwitch(cur.id, m.id, m.enabled === false);
      setSw({ provider: cur.id, models: r.models ?? [] });
      toast(t(m.enabled === false ? "set.modelOnToast" : "set.modelOffToast", { name: m.name || m.id }));
      void refreshModels();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setSwBusy(null);
    }
  };
  const users = cur ? Object.values(agents).filter((a) => (a.models ?? []).some((r) => r.provider === cur.id)) : [];

  return (
    <>
      <Head
        title={t("set.models")}
        sub={t("sub.models")}
        right={
          <>
            <div className="seg">
              <button type="button" aria-pressed={tab === "providers"} onClick={() => setTab("providers")}>
                {t("set.providers")}
              </button>
              <button type="button" aria-pressed={tab === "routing"} onClick={() => setTab("routing")}>
                {t("set.routing")}
              </button>
              <button type="button" aria-pressed={tab === "catalog"} onClick={() => setTab("catalog")}>
                {t("mcat.tab")}
              </button>
            </div>
            <button className="btn" type="button" onClick={() => setAdding(true)}>
              <Plus size={14} />
              {t("set.addProvider")}
            </button>
          </>
        }
      />
      {adding && (
        <AddProviderDialog
          existing={providers.map((p) => p.id)}
          onClose={() => setAdding(false)}
        />
      )}
      {editing && (
        <AddProviderDialog
          existing={providers.map((p) => p.id)}
          edit={editing}
          onClose={() => {
            setEditing(null);
            void loadUser();
          }}
        />
      )}
      {choosingMagpie && <MagpieModelsDialog onClose={() => setChoosingMagpie(false)} />}
      {tab === "routing" ? (
        <Routing />
      ) : tab === "catalog" ? (
        <ModelCatalog />
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
                        <b>{pname(p.id)}</b>
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
              {(() => {
                const up = userProv.find((u) => u.id === cur.id);
                const remove = async () => {
                  if (!up || !(await confirmDialog(t("ap.removeQ", { name: up.name || up.id })))) return;
                  try {
                    await API.removeProvider(up.id);
                    toast(t("ap.removed", { name: up.name || up.id }));
                    await resync();
                    void loadUser();
                  } catch (e: any) {
                    toast(t("ap.failed", { error: e?.message ?? String(e) }));
                  }
                };
                return (
                  <>
                    <div className="pvh">
                      <ProviderMark provider={cur.id} size={40} />
                      <div className="g">
                        <b>
                          {pname(cur.id)}
                          <span className={"pill sm " + (cur.error || isEx(cur) ? "red" : "ok")}>
                            {cur.error ? t("set.errorState") : isEx(cur) ? t("set.exhaustedState") : t("set.connectedState")}
                          </span>
                        </b>
                        <span>
                          {cur.id} · {t(`kind.${cur.kind}` as Key)}
                        </span>
                      </div>
                      {up && (
                        <span className="pvacts">
                          <button className="btn sm" type="button" onClick={() => setEditing(up)}>
                            <Pencil size={13} />
                            {t("ap.editBtn")}
                          </button>
                          <button className="btn sm ghost ib-dng" type="button" aria-label={t("ap.remove")} title={t("ap.remove")} onClick={() => void remove()}>
                            <Trash2 size={14} />
                          </button>
                        </span>
                      )}
                    </div>
                    {up && (
                      <div className="pvmeta">
                        <span>
                          <KeyRound size={13} />
                          <code>{up.masked || "••••"}</code>
                        </span>
                        {up.baseURL && (
                          <span className="mono" title={up.baseURL}>
                            <Globe size={13} />
                            {up.baseURL}
                          </span>
                        )}
                      </div>
                    )}
                  </>
                );
              })()}
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
              {/* 额度 only where the provider tells it (a gateway's
                  accounts, a balance); none for an API that does not */}
              {(cur.quota?.accounts?.length || cur.quota?.known) && (
                <>
                  <div className="psec2">{t("set.quota")}</div>
                  {/* vendor cards bring their own frames; a lone balance sits in the box */}
                  <div className={"box" + (cur.quota.accounts?.length && !(cur.kind !== "gateway" && cur.quota.accounts.length === 1 && cur.quota.accounts[0].balance) ? " qabox" : "")}>
                    {cur.quota.accounts?.length ? (
                      <QuotaAccounts accounts={cur.quota.accounts} solo={cur.kind !== "gateway"} />
                    ) : cur.quota.windows?.length ? (
                      // a gateway fronts several accounts: each window, named
                      cur.quota.windows.map((w) => <QuotaRow key={w.label} label={w.label} remaining={w.remaining} resetsAt={w.resetsAt} />)
                    ) : (
                      <QuotaRow remaining={cur.quota.remaining} resetsAt={cur.quota.resetsAt} />
                    )}
                  </div>
                </>
              )}
              <div className="psec2 psec2r">
                <span>
                  {t("set.availableModels", { n: listed.length })}
                  {listed.some((m) => m.enabled === false) && <span className="small"> · {t("set.modelsOff", { n: listed.filter((m) => m.enabled === false).length })}</span>}
                </span>
                {cur.id === "magpie" ? (
                  <button className="btn sm" type="button" onClick={() => setChoosingMagpie(true)}>
                    <ListChecks size={13} />
                    {t("mp.choose")}
                  </button>
                ) : userProv.some((u) => u.id === cur.id) ? (
                  <button className="btn sm" type="button" onClick={() => setEditing(userProv.find((u) => u.id === cur.id)!)}>
                    <ListChecks size={13} />
                    {t("ap.editModels")}
                  </button>
                ) : (
                  <button className="btn sm" type="button" aria-pressed={editingList} onClick={() => setEditingList(!editingList)}>
                    <ListChecks size={13} />
                    {editingList ? t("set.doneEditing") : t("ap.editModels")}
                  </button>
                )}
              </div>
              {editingList && listed.length > 0 && <p className="small mswnote">{t("set.modelSwitchNote")}</p>}
              {listed.length === 0 ? (
                <div className="empty">{t("set.noModels")}</div>
              ) : (
                <ModelTable provider={cur} models={listed} busy={swBusy} onFlip={flip} switches={editingList} />
              )}
            </div>
          )}
        </div>
      )}
    </>
  );
}

const capName = (c: string) => (t(`set.cap.${c}` as Key) === `set.cap.${c}` ? c : t(`set.cap.${c}` as Key));

/** capabilities at least 80% of the models share */
function commonCaps(list: ModelInfo[]): string[] {
  const n: Record<string, number> = {};
  for (const m of list) for (const c of m.capabilities ?? []) n[c] = (n[c] ?? 0) + 1;
  return Object.keys(n).filter((c) => n[c] >= Math.ceil(list.length * 0.8));
}

/** A provider's models, grouped by what they produce (chat / image / video)
 *  when it serves more than one kind, each with its on/off switch. */
function ModelTable({ provider, models, busy, onFlip, switches = true }: { provider: ProviderView; models: ModelInfo[]; busy: string | null; onFlip: (m: ModelInfo) => void; switches?: boolean }) {
  const kinds = (["chat", "image", "video"] as const).filter((k) => models.some((m) => modelKind(m) === k));
  return (
    <>
      {kinds.map((kind) => {
        const list = models.filter((m) => modelKind(m) === kind);
        // what nearly every model here can do is said once above the list;
        // a row shows only what sets it apart
        const common = commonCaps(list);
        return (
          <div key={kind} className="mkgroup">
            {common.length > 0 && list.length > 3 && (
              <div className="mcommon small">
                {t("set.capsAll")}
                {common.map((c) => (
                  <span key={c} className={"cap c-" + c.split("-")[0]}>
                    {capName(c)}
                  </span>
                ))}
              </div>
            )}
            {kinds.length > 1 && (
              <div className="mkh">
                <span className={"mkind k-" + kind}>{t(`set.kind.${kind}`)}</span>
                <span className="small">
                  {t(`set.kindDesc.${kind}`)} · {list.length}
                </span>
              </div>
            )}
            <div className="mtable">
              {list.map((m) => {
                const on = m.enabled !== false;
                const levels = (m.efforts ?? []).length;
                return (
                  <div className={"mrow" + (on ? "" : " off")} key={m.id}>
                    <ProviderMark provider={provider.id} size={18} />
                    <span className="g">
                      <b>{m.name || m.id}</b>
                      <span className="mono">{m.id}</span>
                    </span>
                    <span className="caps">
                      {(m.capabilities ?? [])
                        .filter((c) => list.length <= 3 || !common.includes(c))
                        .map((c) => (
                          <span key={c} className={"cap c-" + c.split("-")[0]}>
                            {capName(c)}
                          </span>
                        ))}
                      {/* models without something most have: say what is missing */}
                      {list.length > 3 &&
                        common
                          .filter((c) => !(m.capabilities ?? []).includes(c))
                          .map((c) => (
                            <span key={"no" + c} className="cap c-none" title={t("set.capMissing", { cap: capName(c) })}>
                              {t("set.capNo", { cap: capName(c) })}
                            </span>
                          ))}
                      {levels > 1 && (
                        <span className="cap c-reasoning" title={(m.efforts ?? []).join(" · ")}>
                          {t("set.capLevels", { n: levels })}
                        </span>
                      )}
                    </span>
                    <span className="mprice" title={priceLong(m.pricing) || t("mc.noPrice")}>
                      {priceShort(m.pricing) || "—"}
                    </span>
                    {kind === "chat" ? (
                      <span className="mctx" title={m.maxOutput ? t("mc.maxOut", { n: kfmt(m.maxOutput) }) : undefined}>
                        {m.contextWindow ? kfmt(m.contextWindow) : "—"}
                      </span>
                    ) : (
                      <span className={"mkind sm k-" + kind}>{t(`set.kind.${kind}`)}</span>
                    )}
                    {switches && <button
                      className="tg"
                      type="button"
                      role="switch"
                      aria-checked={on}
                      aria-disabled={busy === m.id}
                      aria-label={t("set.modelSwitch", { name: m.name || m.id })}
                      title={on ? t("set.modelOnTitle") : t("set.modelOffTitle")}
                      onClick={() => busy !== m.id && onFlip(m)}
                    />}
                  </div>
                );
              })}
            </div>
          </div>
        );
      })}
    </>
  );
}

function QuotaRow({ label, remaining, resetsAt }: { label?: string; remaining: number; resetsAt?: string }) {
  const pct = Math.round(Math.max(0, Math.min(1, remaining)) * 100);
  const out = pct <= 0;
  return (
    <div className={"qrow" + (label ? "" : " nolabel")}>
      {label && <span>{label}</span>}
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
