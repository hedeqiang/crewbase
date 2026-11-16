import { AlertTriangle, Check, ChevronDown, ExternalLink, KeyRound, Loader2, Search, Sparkles, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { API, desktop, type ModelKind, type ProviderPreset, type ProviderTestResult, type UserProvider } from "../../api";
import { classifyModelId, modelKind } from "../../format";
import { t } from "../../i18n";
import { resync, toast } from "../../store";
import { autoIcon, IconMark, PICKABLE, ProviderMark } from "../ProviderMark";
import { ModelChecklist } from "./ModelChecklist";

type Step = "pick" | "key";

/** Settings › 模型与订阅 › 添加提供方: pick a provider, paste the key, test,
 *  choose models, add. The key goes to the system keychain; the provider is
 *  live immediately, no restart.
 *
 *  With `edit`, the same dialog edits an added provider, opened at the
 *  models step: the listing is fetched again with the stored key, models
 *  can be ticked / unticked or typed by hand, and a blank key keeps the
 *  stored one. */
export function AddProviderDialog({ onClose, existing, edit }: { onClose: () => void; existing: string[]; edit?: UserProvider }) {
  const [presets, setPresets] = useState<ProviderPreset[] | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [step, setStep] = useState<Step>(edit ? "key" : "pick");
  const [q, setQ] = useState("");
  const [pre, setPre] = useState<ProviderPreset | null>(null);
  const [custom, setCustom] = useState(!!edit && !edit.preset);
  const [id, setId] = useState(edit?.id ?? "");
  const [name, setName] = useState(edit?.name ?? "");
  const [baseURL, setBaseURL] = useState(edit?.baseURL ?? "");
  const [api, setApi] = useState(edit?.api ?? "openai");
  const [key, setKey] = useState("");
  /** "" = automatic (detected from the endpoint, name and models) */
  const [icon, setIcon] = useState(edit?.icon ?? "");
  const [iconOpen, setIconOpen] = useState(false);
  const [test, setTest] = useState<ProviderTestResult | null>(null);
  /** ids not in the listing: typed by hand, or enabled before */
  const [extra, setExtra] = useState<string[]>(edit?.models ?? []);
  const [busy, setBusy] = useState<"" | "test" | "add">("");
  const [picked, setPicked] = useState<Set<string>>(new Set(edit?.models ?? []));
  /** model kinds chosen by hand (otherwise: the listing's, else by id) */
  const [kinds, setKinds] = useState<Record<string, ModelKind>>((edit?.kinds as Record<string, ModelKind>) ?? {});
  const keyRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    API.providers()
      .then((r) => {
        setPresets(r.presets ?? []);
        if (edit?.preset) setPre(r.presets?.find((p) => p.id === edit.preset) ?? null);
      })
      .catch((e: any) => {
        setPresets([]);
        // an older daemon without the providers API answers 404
        setLoadErr(e?.status === 404 ? t("ap.oldDaemon") : t("ap.loadFailed", { error: e?.message ?? String(e) }));
      });
    // editing: list the provider's models again with the stored key
    if (edit) void runTest();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  useEffect(() => {
    if (step === "key" && !edit) keyRef.current?.focus();
  }, [step, edit]);

  const list = useMemo(() => {
    const ql = q.trim().toLowerCase();
    return (presets ?? []).filter((p) => !ql || p.name.toLowerCase().includes(ql) || p.id.includes(ql));
  }, [presets, q]);
  const vendors = list.filter((p) => !p.group);
  const platforms = list.filter((p) => p.group === "platform");
  const showCustom = !q.trim() || t("ap.custom").toLowerCase().includes(q.trim().toLowerCase()) || !list.length;

  const choose = (p: ProviderPreset | null) => {
    setPre(p);
    setCustom(!p);
    setId(p?.id ?? "");
    setName(p?.name ?? "");
    setBaseURL(p?.baseURL ?? "");
    setApi(p?.api ?? "openai");
    setIcon(p?.icon ?? "");
    setTest(null);
    setExtra([]);
    setPicked(new Set());
    setKinds({});
    setStep("key");
  };

  const detected = useMemo(() => autoIcon(baseURL, name, test?.models?.map((m) => m.id) ?? []), [baseURL, name, test]);
  const effIcon = icon || detected;
  // the listing is unavailable: models are typed by hand (editing: also
  // when the re-fetch failed, so the stored list stays editable)
  const manualMode = !!test && !test.ok && (!!test.manualModels || !!edit);
  const listing = test?.ok ? test.models : [];
  const all = [...listing.map((m) => m.id), ...extra.filter((x) => !listing.some((m) => m.id === x))];
  const chosen = all.filter((m) => picked.has(m));

  const input = () => ({
    id: id.trim(),
    name: name.trim(),
    preset: edit ? (edit.preset ?? "") : (pre?.id ?? ""),
    api,
    baseURL: baseURL.trim(),
    apiKey: key,
    icon: custom || edit ? effIcon : (pre?.icon ?? ""),
  });

  const runTest = async () => {
    setBusy("test");
    setTest(null);
    try {
      const r = await API.testProvider(input());
      r.models = r.models ?? [];
      setTest(r);
      // start with two chat models; image / video models are opt-in
      // (editing keeps what is enabled)
      if (r.ok && !edit) setPicked(new Set(r.models.filter((m) => modelKind(m) === "chat").slice(0, 2).map((m) => m.id)));
    } catch (e: any) {
      setTest({ ok: false, latencyMs: 0, models: [], error: e?.message ?? String(e) });
    } finally {
      setBusy("");
    }
  };

  const add = async () => {
    setBusy("add");
    try {
      const models = chosen;
      // send only kinds the daemon would not derive from the id itself
      const marks: Record<string, ModelKind> = {};
      for (const m of models) if (kindOf(m) !== classifyModelId(m)) marks[m] = kindOf(m);
      if (edit) {
        const inp = input();
        await API.updateProvider(edit.id, { name: inp.name, icon: inp.icon, baseURL: inp.baseURL, apiKey: key || undefined, models, kinds: marks });
        toast(t("ap.saved", { name: name || id }));
      } else {
        await API.addProvider({ ...input(), models, kinds: marks });
        toast(t("ap.added", { name: name || id }));
      }
      await resync();
      onClose();
    } catch (e: any) {
      toast(t("ap.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy("");
    }
  };

  const kindOf = (m: string): ModelKind => {
    if (kinds[m]) return kinds[m];
    const listed = test?.models?.find((x) => x.id === m);
    return listed ? modelKind(listed) : classifyModelId(m);
  };
  const kindSelect = (m: string) => (
    <select
      className="ap-kind"
      aria-label={t("ap.kindOf", { id: m })}
      value={kindOf(m)}
      onClick={(e) => e.stopPropagation()}
      onChange={(e) => setKinds((k) => ({ ...k, [m]: e.target.value as ModelKind }))}
    >
      <option value="chat">{t("set.kind.chat")}</option>
      <option value="image">{t("set.kind.image")}</option>
      <option value="video">{t("set.kind.video")}</option>
    </select>
  );

  const open = (url?: string) => {
    if (!url) return;
    const d = desktop();
    if (d?.openExternal) d.openExternal(url);
    else window.open(url, "_blank", "noopener");
  };

  const addIds = (ids: string[]) => {
    setExtra((x) => [...x, ...ids.filter((i) => !x.includes(i))]);
    setPicked((s) => new Set([...s, ...ids]));
  };

  const idTaken = !edit && !!id && existing.includes(id) && !test?.ok;
  const showIdent = custom || !!edit;

  const card = (p: ProviderPreset) => {
    const added = existing.includes(p.id);
    return (
      <button key={p.id} type="button" className="ap-card" disabled={added} onClick={() => choose(p)} title={added ? t("ap.already") : p.baseURL}>
        {p.icon ? <IconMark icon={p.icon} id={p.id} label={p.name} size={22} /> : <ProviderMark provider={p.id} size={22} />}
        <span>{p.name}</span>
        {added && <Check size={14} className="ap-done" />}
      </button>
    );
  };

  return (
    <>
      <div className="ovscrim show" onMouseDown={onClose} />
      <div className="modal addprov" role="dialog" aria-modal="true" aria-label={edit ? t("ap.edit", { name: edit.name || edit.id }) : t("set.addProvider")}>
        <div className="mhd">
          <h3>{edit ? t("ap.edit", { name: edit.name || edit.id }) : step === "pick" ? t("set.addProvider") : t("ap.connect", { name: name || t("ap.custom") })}</h3>
          <button className="ib" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={16} />
          </button>
        </div>
        <div className="mbd">
          {step === "pick" ? (
            <>
              <label className="search ap-search">
                <Search size={14} />
                <input autoFocus value={q} placeholder={t("ap.search")} onChange={(e) => setQ(e.target.value)} />
              </label>
              {loadErr && (
                <div className="ap-warn">
                  <AlertTriangle size={15} />
                  <span>{loadErr}</span>
                </div>
              )}
              {presets === null && !loadErr && (
                <div className="small ap-loading">
                  <Loader2 size={14} className="spin" /> {t("common.loading")}
                </div>
              )}
              {vendors.length > 0 && (
                <>
                  <div className="ap-sec">{t("ap.vendors")}</div>
                  <div className="ap-grid">{vendors.map(card)}</div>
                </>
              )}
              {(platforms.length > 0 || showCustom) && <div className="ap-sec">{t("ap.platforms")}</div>}
              <div className="ap-grid">
                {platforms.map(card)}
                {showCustom && (
                  <button type="button" className="ap-card" disabled={!!loadErr} onClick={() => choose(null)}>
                    <span className="ap-plus">+</span>
                    <span>{t("ap.custom")}</span>
                  </button>
                )}
              </div>
              <p className="small ap-note">{t("ap.subNote")}</p>
            </>
          ) : (
            <>
              {showIdent && (
                <div className="ap-ident">
                  <div className="ap-iconpick">
                    <button
                      type="button"
                      className={"ap-iconbtn" + (iconOpen ? " on" : "")}
                      aria-haspopup="listbox"
                      aria-expanded={iconOpen}
                      title={t("ap.icon")}
                      onClick={() => setIconOpen((v) => !v)}
                    >
                      <IconMark icon={effIcon} id={id || "custom"} label={name || id || "?"} size={28} />
                      <ChevronDown size={12} />
                    </button>
                    <span className="ap-iconhint">
                      {icon ? (
                        <button type="button" className="btn sm ghost" onClick={() => setIcon("")}>
                          <Sparkles size={11} /> {t("ap.iconAutoBack")}
                        </button>
                      ) : (
                        <>
                          <Sparkles size={11} /> {t("ap.iconAuto")}
                        </>
                      )}
                    </span>
                  </div>
                  <div className="ap-row2 grow">
                    <label className="fl">
                      <span>{t("ap.id")}</span>
                      <input className="fi sm" value={id} disabled={!!edit} placeholder="my-provider" onChange={(e) => setId(e.target.value.toLowerCase())} />
                    </label>
                    <label className="fl">
                      <span>{t("ap.name")}</span>
                      <input className="fi sm" value={name} placeholder={t("ap.namePh")} onChange={(e) => setName(e.target.value)} />
                    </label>
                  </div>
                </div>
              )}
              {showIdent && iconOpen && (
                <div className="ap-icons" role="listbox" aria-label={t("ap.icon")}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={!icon}
                    className={"ap-ic auto" + (!icon ? " sel" : "")}
                    title={t("ap.iconAuto")}
                    onClick={() => {
                      setIcon("");
                      setIconOpen(false);
                    }}
                  >
                    <Sparkles size={16} />
                  </button>
                  <button
                    type="button"
                    role="option"
                    aria-selected={icon === "mono"}
                    className={"ap-ic" + (icon === "mono" ? " sel" : "")}
                    title={t("ap.iconMono")}
                    onClick={() => {
                      setIcon("mono");
                      setIconOpen(false);
                    }}
                  >
                    <IconMark icon="mono" id={id || "custom"} label={name || id || "?"} size={20} />
                  </button>
                  {PICKABLE.map((p) => (
                    <button
                      key={p.key}
                      type="button"
                      role="option"
                      aria-selected={icon === p.key}
                      className={"ap-ic" + (icon === p.key ? " sel" : "")}
                      title={p.name}
                      onClick={() => {
                        setIcon(p.key);
                        setIconOpen(false);
                      }}
                    >
                      <IconMark icon={p.key} id={p.key} label={p.name} size={20} />
                    </button>
                  ))}
                </div>
              )}
              <label className="fl">
                <span className="flh">
                  {t("ap.key")}
                  {pre?.keyURL && (
                    <button type="button" className="linkbtn" onClick={() => open(pre.keyURL)}>
                      {t("ap.getKey")} <ExternalLink size={12} />
                    </button>
                  )}
                </span>
                <span className="iwrap fi sm">
                  <KeyRound size={14} />
                  <input
                    ref={keyRef}
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    value={key}
                    placeholder={edit ? t("ap.keyKeep", { masked: edit.masked || "••••" }) : "sk-…"}
                    onChange={(e) => {
                      setKey(e.target.value);
                      if (!edit) setTest(null);
                    }}
                    onKeyDown={(e) => e.key === "Enter" && (key || edit) && void runTest()}
                  />
                </span>
                <span className="small">{edit ? t("ap.keyKeepNote") : t("ap.keyNote")}</span>
              </label>
              <label className="fl">
                <span>{t("ap.base")}</span>
                <input className="fi sm mono" value={baseURL} placeholder="https://…/v1" onChange={(e) => setBaseURL(e.target.value)} />
              </label>
              {custom && !edit && (
                <label className="fl">
                  <span>{t("ap.api")}</span>
                  <select className="fi sm" value={api} onChange={(e) => setApi(e.target.value)}>
                    <option value="openai">OpenAI Chat Completions</option>
                    <option value="anthropic">Anthropic Messages</option>
                    <option value="gemini">Google Gemini</option>
                  </select>
                </label>
              )}

              {test && !test.ok && !manualMode && <div className="confirmbar errbar">{test.error || t("ap.testFailed")}</div>}
              {(manualMode || test?.ok) && (
                <div className="ap-models">
                  <div className="ap-mh">
                    {test?.ok ? (
                      <span className="okt">
                        <Check size={14} /> {t("ap.connected", { ms: test.latencyMs, n: test.models.length })}
                      </span>
                    ) : (
                      <span className="amt">
                        <AlertTriangle size={14} /> {test?.manualModels ? t("ap.noList") : t("ap.listFailed")}
                      </span>
                    )}
                    {edit && (
                      <button type="button" className="btn sm ghost" disabled={busy !== ""} onClick={() => void runTest()}>
                        {t("ap.refetch")}
                      </button>
                    )}
                  </div>
                  <ModelChecklist
                    models={listing}
                    extra={extra}
                    picked={picked}
                    setPicked={setPicked}
                    kindOf={kindOf}
                    kindSelect={kindSelect}
                    onAdd={addIds}
                    addPlaceholder={pre?.modelHint}
                    autoFocus={!edit}
                  />
                  <p className="small">
                    {manualMode ? t("ap.manualNote") : t("ap.modelsNote")}
                    {manualMode && test?.error ? <span className="mono ap-err"> {test.error}</span> : null}
                  </p>
                </div>
              )}
              {edit && busy === "test" && (
                <div className="small ap-loading">
                  <Loader2 size={14} className="spin" /> {t("ap.refetching")}
                </div>
              )}
              {idTaken && <p className="small dngt">{t("ap.idTaken", { id })}</p>}

              <div className="mft">
                <button className="btn" type="button" onClick={() => (edit ? onClose() : setStep("pick"))}>
                  {edit ? t("common.cancel") : t("ap.back")}
                </button>
                {test?.ok || manualMode ? (
                  <button className="btn pri" type="button" disabled={busy !== "" || chosen.length === 0} onClick={() => void add()}>
                    {busy === "add" ? <Loader2 size={14} className="spin" /> : null}
                    {edit ? t("ap.save", { n: chosen.length }) : t("ap.add", { n: chosen.length })}
                  </button>
                ) : (
                  <button className="btn pri" type="button" disabled={busy !== "" || (!key && !edit) || !id || !baseURL} onClick={() => void runTest()}>
                    {busy === "test" ? <Loader2 size={14} className="spin" /> : null}
                    {t("ap.test")}
                  </button>
                )}
              </div>
            </>
          )}
        </div>
      </div>
    </>
  );
}
