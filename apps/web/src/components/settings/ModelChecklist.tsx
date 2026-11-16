import { Plus, Search, X } from "lucide-react";
import { useMemo, useRef, useState, type ReactNode } from "react";
import type { ModelInfo, ModelKind } from "../../api";
import { classifyModelId, modelKind, modelMeta, priceLong } from "../../format";
import { t } from "../../i18n";

const KINDS: ModelKind[] = ["chat", "image", "video"];
const idRe = /^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,159}$/;

/** Model ids typed by hand: one per line or separated by commas / spaces. */
export function parseIds(s: string): string[] {
  return [...new Set(s.split(/[\s,，]+/).map((x) => x.trim()).filter(Boolean))];
}

/** A searchable checklist of models: filter by id / name, select all or
 *  none of what is shown, "已选 N / 共 M", grouped by kind when the list
 *  has more than one, each row with its context window and list price.
 *  Used by the add / edit provider dialog and the magpie chooser. */
export function ModelChecklist({
  models,
  extra = [],
  picked,
  setPicked,
  kindOf,
  kindSelect,
  onAdd,
  addPlaceholder,
  autoFocus,
}: {
  /** the provider's listing */
  models: ModelInfo[];
  /** ids not in the listing (already enabled, or typed by hand) */
  extra?: string[];
  picked: Set<string>;
  setPicked: (s: Set<string>) => void;
  /** kind for grouping (default: the listing's, else by id) */
  kindOf?: (id: string) => ModelKind;
  /** a per-row control to mark the kind by hand */
  kindSelect?: (id: string) => ReactNode;
  /** offer "add a model id by hand"; called with the new ids */
  onAdd?: (ids: string[]) => void;
  addPlaceholder?: string;
  autoFocus?: boolean;
}) {
  const [q, setQ] = useState("");
  const [typed, setTyped] = useState("");
  const listRef = useRef<HTMLDivElement>(null);
  const info = useMemo(() => new Map(models.map((m) => [m.id, m])), [models]);
  const ids = useMemo(() => [...models.map((m) => m.id), ...extra.filter((x) => !info.has(x))], [models, extra, info]);
  const kind = (id: string): ModelKind => kindOf?.(id) ?? (info.has(id) ? modelKind(info.get(id)) : classifyModelId(id));
  const ql = q.trim().toLowerCase();
  const shown = ids.filter((id) => !ql || id.toLowerCase().includes(ql) || (info.get(id)?.name ?? "").toLowerCase().includes(ql));
  const kinds = KINDS.filter((k) => ids.some((id) => kind(id) === k));
  const nPicked = ids.filter((id) => picked.has(id)).length;
  const allShownOn = shown.length > 0 && shown.every((id) => picked.has(id));

  const setShown = (on: boolean) => {
    const n = new Set(picked);
    for (const id of shown) {
      if (on) n.add(id);
      else n.delete(id);
    }
    setPicked(n);
  };
  const toggle = (id: string) => {
    const n = new Set(picked);
    if (n.has(id)) n.delete(id);
    else n.add(id);
    setPicked(n);
  };
  const typedIds = parseIds(typed);
  const typedBad = typedIds.filter((x) => !idRe.test(x));
  const addTyped = () => {
    if (!onAdd || !typedIds.length || typedBad.length) return;
    onAdd(typedIds);
    setTyped("");
    setQ("");
    // show what was added (it goes to the end of the list)
    requestAnimationFrame(() => listRef.current?.scrollTo({ top: listRef.current.scrollHeight }));
  };

  const row = (id: string) => {
    const m = info.get(id);
    const meta = modelMeta(m ?? { contextWindow: 0, kind: kind(id) });
    const named = m?.name && m.name !== id;
    return (
      <label key={id} className="ap-m">
        <input type="checkbox" checked={picked.has(id)} onChange={() => toggle(id)} />
        <span className="ap-mn">
          {named ? (
            <>
              <b>{m!.name}</b>
              <span className="mono">{id}</span>
            </>
          ) : (
            <span className="mono">{id}</span>
          )}
        </span>
        {!m && <span className="ap-tag">{t("mc.manual")}</span>}
        {meta && (
          <span className="ap-meta" title={priceLong(m?.pricing) || undefined}>
            {meta}
          </span>
        )}
        {kindSelect?.(id)}
      </label>
    );
  };

  return (
    <div className="mcl">
      <div className="mcl-bar">
        <label className="search mcl-q">
          <Search size={14} />
          <input autoFocus={autoFocus} value={q} placeholder={t("mc.search")} aria-label={t("mc.search")} onChange={(e) => setQ(e.target.value)} />
          {q && (
            <button type="button" className="ib sm" aria-label={t("mc.clear")} onClick={() => setQ("")}>
              <X size={12} />
            </button>
          )}
        </label>
        <span className="small mcl-n">
          {t("mc.count", { n: nPicked, m: ids.length })}
          {ql ? ` · ${t("mc.matching", { k: shown.length })}` : ""}
        </span>
        <button type="button" className="linkbtn" disabled={!shown.length} onClick={() => setShown(!allShownOn)}>
          {allShownOn ? (ql ? t("mc.noneShown") : t("ap.none")) : ql ? t("mc.allShown") : t("ap.all")}
        </button>
      </div>
      <div className="ap-ml" ref={listRef}>
        {shown.length === 0 && <div className="small mcl-empty">{ids.length ? t("mc.noMatch") : t("mc.empty")}</div>}
        {kinds.length > 1
          ? kinds.map((k) => {
              const list = shown.filter((id) => kind(id) === k);
              if (!list.length) return null;
              return (
                <div key={k} className="mcl-g">
                  <div className="mcl-gh">
                    <span className={"mkind sm k-" + k}>{t(`set.kind.${k}`)}</span>
                    <span className="small">
                      {list.filter((id) => picked.has(id)).length} / {list.length}
                    </span>
                  </div>
                  {list.map(row)}
                </div>
              );
            })
          : shown.map(row)}
      </div>
      {onAdd && (
        <div className="mcl-add">
          <input
            className="fi sm mono"
            value={typed}
            placeholder={addPlaceholder || t("mc.addPh")}
            aria-label={t("mc.addPh")}
            onChange={(e) => setTyped(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addTyped();
              }
            }}
          />
          <button type="button" className="btn sm" disabled={!typedIds.length || typedBad.length > 0} onClick={addTyped}>
            <Plus size={13} /> {t("mc.add")}
          </button>
        </div>
      )}
      {typedBad.length > 0 && <p className="small dngt">{t("mc.badId", { id: typedBad[0] })}</p>}
    </div>
  );
}
