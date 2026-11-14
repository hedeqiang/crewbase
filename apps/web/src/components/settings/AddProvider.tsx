import { Check, ExternalLink, KeyRound, Loader2, Search, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { API, desktop, type ProviderPreset, type ProviderTestResult } from "../../api";
import { t } from "../../i18n";
import { resync, toast } from "../../store";
import { ProviderMark } from "../ProviderMark";

type Step = "pick" | "key";

/** Settings › 模型与订阅 › 添加提供方: pick a provider, paste the key, test,
 *  choose models, add. The key goes to the system keychain; the provider is
 *  live immediately, no restart. */
export function AddProviderDialog({ onClose, existing }: { onClose: () => void; existing: string[] }) {
  const [presets, setPresets] = useState<ProviderPreset[]>([]);
  const [step, setStep] = useState<Step>("pick");
  const [q, setQ] = useState("");
  const [pre, setPre] = useState<ProviderPreset | null>(null);
  const [custom, setCustom] = useState(false);
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [baseURL, setBaseURL] = useState("");
  const [api, setApi] = useState("openai");
  const [key, setKey] = useState("");
  const [test, setTest] = useState<ProviderTestResult | null>(null);
  const [busy, setBusy] = useState<"" | "test" | "add">("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const keyRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    API.providers()
      .then((r) => setPresets(r.presets))
      .catch(() => {});
  }, []);
  useEffect(() => {
    if (step === "key") keyRef.current?.focus();
  }, [step]);

  const list = useMemo(() => {
    const ql = q.trim().toLowerCase();
    return presets.filter((p) => !ql || p.name.toLowerCase().includes(ql) || p.id.includes(ql));
  }, [presets, q]);

  const choose = (p: ProviderPreset | null) => {
    setPre(p);
    setCustom(!p);
    setId(p?.id ?? "");
    setName(p?.name ?? "");
    setBaseURL(p?.baseURL ?? "");
    setApi(p?.api ?? "openai");
    setTest(null);
    setStep("key");
  };

  const input = () => ({ id: id.trim(), name: name.trim(), preset: pre?.id ?? "", api, baseURL: baseURL.trim(), apiKey: key });

  const runTest = async () => {
    setBusy("test");
    setTest(null);
    try {
      const r = await API.testProvider(input());
      setTest(r);
      if (r.ok) setPicked(new Set(r.models.slice(0, 2).map((m) => m.id)));
    } catch (e: any) {
      setTest({ ok: false, latencyMs: 0, models: [], error: e?.message ?? String(e) });
    } finally {
      setBusy("");
    }
  };

  const add = async () => {
    setBusy("add");
    try {
      await API.addProvider({ ...input(), models: [...picked] });
      toast(t("ap.added", { name: name || id }));
      await resync();
      onClose();
    } catch (e: any) {
      toast(t("ap.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy("");
    }
  };

  const open = (url?: string) => {
    if (!url) return;
    const d = desktop();
    if (d?.openExternal) d.openExternal(url);
    else window.open(url, "_blank", "noopener");
  };

  const toggle = (m: string) =>
    setPicked((s) => {
      const n = new Set(s);
      if (n.has(m)) n.delete(m);
      else n.add(m);
      return n;
    });

  const idTaken = !!id && existing.includes(id) && !test?.ok;

  return (
    <>
      <div className="ovscrim show" onMouseDown={onClose} />
      <div className="modal addprov" role="dialog" aria-modal="true" aria-label={t("set.addProvider")}>
        <div className="mhd">
          <h3>{step === "pick" ? t("set.addProvider") : t("ap.connect", { name: name || t("ap.custom") })}</h3>
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
              <div className="ap-grid">
                {list.map((p) => {
                  const added = existing.includes(p.id);
                  return (
                    <button key={p.id} type="button" className="ap-card" disabled={added} onClick={() => choose(p)} title={added ? t("ap.already") : p.baseURL}>
                      <ProviderMark provider={p.id} size={22} />
                      <span>{p.name}</span>
                      {added && <Check size={14} className="ap-done" />}
                    </button>
                  );
                })}
                <button type="button" className="ap-card" onClick={() => choose(null)}>
                  <span className="ap-plus">+</span>
                  <span>{t("ap.custom")}</span>
                </button>
              </div>
              <p className="small ap-note">{t("ap.subNote")}</p>
            </>
          ) : (
            <>
              {custom && (
                <div className="ap-row2">
                  <label className="fl">
                    <span>{t("ap.id")}</span>
                    <input className="fi sm" value={id} placeholder="my-provider" onChange={(e) => setId(e.target.value.toLowerCase())} />
                  </label>
                  <label className="fl">
                    <span>{t("ap.name")}</span>
                    <input className="fi sm" value={name} placeholder={t("ap.namePh")} onChange={(e) => setName(e.target.value)} />
                  </label>
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
                    placeholder="sk-…"
                    onChange={(e) => {
                      setKey(e.target.value);
                      setTest(null);
                    }}
                    onKeyDown={(e) => e.key === "Enter" && key && void runTest()}
                  />
                </span>
                <span className="small">{t("ap.keyNote")}</span>
              </label>
              <label className="fl">
                <span>{t("ap.base")}</span>
                <input className="fi sm mono" value={baseURL} placeholder="https://…/v1" onChange={(e) => setBaseURL(e.target.value)} />
              </label>
              {custom && (
                <label className="fl">
                  <span>{t("ap.api")}</span>
                  <select className="fi sm" value={api} onChange={(e) => setApi(e.target.value)}>
                    <option value="openai">OpenAI Chat Completions</option>
                    <option value="anthropic">Anthropic Messages</option>
                    <option value="gemini">Google Gemini</option>
                  </select>
                </label>
              )}

              {test && !test.ok && <div className="confirmbar errbar">{test.error || t("ap.testFailed")}</div>}
              {test?.ok && (
                <div className="ap-models">
                  <div className="ap-mh">
                    <span className="okt">
                      <Check size={14} /> {t("ap.connected", { ms: test.latencyMs, n: test.models.length })}
                    </span>
                    <button type="button" className="linkbtn" onClick={() => setPicked(new Set(picked.size === test.models.length ? [] : test.models.map((m) => m.id)))}>
                      {picked.size === test.models.length ? t("ap.none") : t("ap.all")}
                    </button>
                  </div>
                  <div className="ap-ml">
                    {test.models.map((m) => (
                      <label key={m.id} className="ap-m">
                        <input type="checkbox" checked={picked.has(m.id)} onChange={() => toggle(m.id)} />
                        <span className="mono">{m.id}</span>
                      </label>
                    ))}
                  </div>
                  <p className="small">{t("ap.modelsNote")}</p>
                </div>
              )}
              {idTaken && <p className="small dngt">{t("ap.idTaken", { id })}</p>}

              <div className="mft">
                <button className="btn" type="button" onClick={() => setStep("pick")}>
                  {t("ap.back")}
                </button>
                {test?.ok ? (
                  <button className="btn pri" type="button" disabled={busy !== "" || picked.size === 0} onClick={() => void add()}>
                    {busy === "add" ? <Loader2 size={14} className="spin" /> : null}
                    {t("ap.add", { n: picked.size })}
                  </button>
                ) : (
                  <button className="btn pri" type="button" disabled={busy !== "" || !key || !id || !baseURL} onClick={() => void runTest()}>
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
