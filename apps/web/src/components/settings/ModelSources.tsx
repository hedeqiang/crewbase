import { Loader2, RefreshCw, X } from "lucide-react";
import { useEffect, useState } from "react";
import { API, type CatalogStatus, type MagpieChoice } from "../../api";
import { ago } from "../../format";
import { t } from "../../i18n";
import { resync, toast } from "../../store";
import { Row } from "../Settings";
import { ModelChecklist } from "./ModelChecklist";

/** Choose which of magpie's models Crewbase offers (default: all). */
export function MagpieModelsDialog({ onClose }: { onClose: () => void }) {
  const [ch, setCh] = useState<MagpieChoice | null>(null);
  const [err, setErr] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    API.magpieModels().then(
      (r) => {
        setCh(r);
        setPicked(new Set(r.enabled ?? r.models.map((m) => m.id)));
      },
      (e: any) => setErr(e?.message ?? String(e)),
    );
  }, []);
  const extra = (ch?.enabled ?? []).filter((id) => !ch?.models.some((m) => m.id === id));
  const save = async (all: boolean) => {
    if (!ch) return;
    setBusy(true);
    try {
      const ids = [...ch.models.map((m) => m.id), ...extra].filter((id) => picked.has(id));
      // everything ticked = "all" (new magpie models then show up too)
      const everything = all || (extra.length === 0 && ids.length === ch.models.length);
      await API.setMagpieModels(everything ? null : ids);
      toast(t("mp.saved", { n: everything ? ch.models.length : ids.length }));
      await resync();
      onClose();
    } catch (e: any) {
      toast(t("ap.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <div className="ovscrim show" onMouseDown={onClose} />
      <div className="modal addprov" role="dialog" aria-modal="true" aria-label={t("mp.title")}>
        <div className="mhd">
          <h3>{t("mp.title")}</h3>
          <button className="ib" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={16} />
          </button>
        </div>
        <div className="mbd">
          <p className="small mp-desc">{t("mp.desc")}</p>
          {err && <div className="confirmbar errbar">{err}</div>}
          {!ch && !err && (
            <div className="small ap-loading">
              <Loader2 size={14} className="spin" /> {t("common.loading")}
            </div>
          )}
          {ch && !ch.running && <div className="confirmbar errbar">{t("mp.notRunning")}</div>}
          {ch && (
            <div className="ap-models">
              <ModelChecklist models={ch.models} extra={extra} picked={picked} setPicked={setPicked} autoFocus />
              <p className="small">{t("mp.note")}</p>
            </div>
          )}
          <div className="mft">
            {ch?.enabled != null && (
              <button className="btn" type="button" disabled={busy} onClick={() => void save(true)} title={t("mp.resetDesc")}>
                {t("mp.reset")}
              </button>
            )}
            <span className="grow" />
            <button className="btn" type="button" onClick={onClose}>
              {t("common.cancel")}
            </button>
            <button className="btn pri" type="button" disabled={busy || !ch || picked.size === 0} onClick={() => void save(false)}>
              {busy ? <Loader2 size={14} className="spin" /> : null}
              {t("common.save")}
            </button>
          </div>
        </div>
      </div>
    </>
  );
}

/** "3 分钟前" / "刚刚" */
const when = (iso: string) => {
  const a = ago(iso);
  return a === t("time.justNow") ? a : t("mcat.ago", { time: a });
};

const srcName =(st: CatalogStatus["sources"][number]) =>
  st.format === "openrouter" ? "OpenRouter" : st.format === "litellm" ? "LiteLLM" : st.url.replace(/^https?:\/\//, "").split("/")[0];

/** Settings › 模型与订阅 › 模型资料: where context windows and prices come
 *  from, when they were updated, 立即更新, and the source URLs. */
export function ModelCatalog() {
  const [st, setSt] = useState<CatalogStatus | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [draft, setDraft] = useState<string | null>(null);
  const load = () =>
    API.modelCatalog().then(
      (r) => {
        setSt(r);
        setErr("");
      },
      (e: any) => setErr(e?.status === 404 ? t("ap.oldDaemon") : (e?.message ?? String(e))),
    );
  useEffect(() => {
    void load();
  }, []);
  const refresh = async () => {
    setBusy(true);
    try {
      const r = await API.refreshModelCatalog();
      setSt(r);
      const failed = r.sources.filter((s) => s.error);
      toast(failed.length ? t("mcat.partial", { n: failed.length }) : t("mcat.done", { n: r.models }));
      await resync();
    } catch (e: any) {
      toast(t("ap.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
    }
  };
  const save = async (b: { sources?: string[]; autoRefresh?: boolean }) => {
    if (!st) return;
    try {
      const r = await API.setModelCatalog({ sources: b.sources ?? st.settings.sources, autoRefresh: b.autoRefresh ?? st.settings.autoRefresh });
      setSt(r);
      setDraft(null);
      toast(t("panel.saved"));
      await resync();
    } catch (e: any) {
      toast(t("ap.failed", { error: e?.message ?? String(e) }));
    }
  };
  if (err) return <div className="confirmbar errbar">{err}</div>;
  if (!st)
    return (
      <div className="small ap-loading">
        <Loader2 size={14} className="spin" /> {t("common.loading")}
      </div>
    );
  const offline = st.settings.sources.length === 0;
  const defaults = ["https://openrouter.ai/api/v1/models", "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"];
  return (
    <div className="vb">
      <p className="small mcat-desc">{t("mcat.desc")}</p>
      <div className="sbox">
        <Row
          title={t("mcat.status")}
          desc={
            offline
              ? t("mcat.offlineDesc", { n: st.models })
              : t("mcat.statusDesc", { n: st.models, time: st.updated && !st.updated.startsWith("0001") ? when(st.updated) : t("mcat.never") })
          }
        >
          <button className="btn" type="button" disabled={busy || offline || st.refreshing} onClick={() => void refresh()}>
            {busy || st.refreshing ? <Loader2 size={14} className="spin" /> : <RefreshCw size={14} />}
            {busy || st.refreshing ? t("mcat.updating") : t("mcat.update")}
          </button>
        </Row>
        {st.sources.map((s) => (
          <div key={s.url} className="mcat-src">
            <span className="g">
              <b>{srcName(s)}</b>
              <span className="mono">{s.url}</span>
            </span>
            <span className="small mcat-st">
              {s.origin === "remote"
                ? t("mcat.remote", { n: s.models, time: s.fetched ? when(s.fetched) : "" })
                : s.origin === "embedded"
                  ? t("mcat.embedded", { n: s.models })
                  : t("mcat.none")}
            </span>
            {s.error && (
              <span className="mcat-err" title={s.error}>
                {t("mcat.failed", { error: s.error })}
              </span>
            )}
          </div>
        ))}
        <Row title={t("mcat.auto")} desc={t("mcat.autoDesc")}>
          <button
            type="button"
            className="tg"
            role="switch"
            aria-checked={st.settings.autoRefresh && !offline}
            aria-label={t("mcat.auto")}
            disabled={offline}
            onClick={() => void save({ autoRefresh: !st.settings.autoRefresh })}
          />
        </Row>
        <div className="mcat-urls">
          <div className="flh">
            <b>{t("mcat.sources")}</b>
            <span className="small">{t("mcat.sourcesDesc")}</span>
          </div>
          <textarea
            className="fi sm mono"
            rows={3}
            spellCheck={false}
            aria-label={t("mcat.sources")}
            value={draft ?? st.settings.sources.join("\n")}
            placeholder={t("mcat.sourcesPh")}
            onChange={(e) => setDraft(e.target.value)}
          />
          <div className="mcat-act">
            <button type="button" className="btn sm ghost" onClick={() => setDraft(defaults.join("\n"))}>
              {t("mcat.defaults")}
            </button>
            <span className="grow" />
            {draft !== null && (
              <>
                <button className="btn sm" type="button" onClick={() => setDraft(null)}>
                  {t("common.cancel")}
                </button>
                <button
                  className="btn pri sm"
                  type="button"
                  onClick={() =>
                    void save({
                      sources: draft
                        .split(/\s+/)
                        .map((x) => x.trim())
                        .filter(Boolean),
                    })
                  }
                >
                  {t("common.save")}
                </button>
              </>
            )}
          </div>
        </div>
      </div>
      <p className="small mcat-note">{t("mcat.privacy")}</p>
    </div>
  );
}
