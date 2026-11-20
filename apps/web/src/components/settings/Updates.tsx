// 插件与技能 › 更新: the add-ons with a newer version (features/addonupdates).
// Quiet by design: a dot on the settings button and a count in the menu
// lead here; nothing pops up. Plugins show their versions; skills and apps
// only that there is an update. An update asking for more permissions is
// installed separately and waits for approval.

import { useEffect, useState } from "react";
import { ArrowRight, Check, ChevronDown, ChevronRight, RefreshCw, ShieldAlert, Sparkles } from "lucide-react";
import { API, type AddonUpdate, type AddonUpdates, type PluginView } from "../../api";
import { t } from "../../i18n";
import { setState, toast, useStore } from "../../store";
import { Markdown } from "../Markdown";

const key = (u: AddonUpdate) => `${u.kind}:${u.agent ?? ""}:${u.id}`;

export function useAddonUpdates() {
  const [d, setD] = useState<AddonUpdates | null>(null);
  const count = useStore((s) => s.addonUpdates);
  const load = () =>
    API.addonUpdates().then(
      (x) => {
        setD(x);
        setState({ addonUpdates: x.items.length });
      },
      () => setD({ items: [] }),
    );
  // the server pushes the count when the list changes
  useEffect(() => {
    void load();
  }, [count]);
  return { d, setD, load };
}

export function UpdatesSection({ u, onChanged, onApprove }: { u: ReturnType<typeof useAddonUpdates>; onChanged: () => void; onApprove: (p: PluginView) => void }) {
  const { d, setD } = u;
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [done, setDone] = useState<Record<string, { name: string; text: string }>>({});
  const [errs, setErrs] = useState<Record<string, string>>({});
  const [open, setOpen] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const items = d?.items ?? [];
  const doneItems = Object.keys(done);

  const apply = async (x: AddonUpdate): Promise<boolean> => {
    const k = key(x);
    setBusy((b) => ({ ...b, [k]: true }));
    setErrs((e) => ({ ...e, [k]: "" }));
    try {
      const r = await API.applyAddonUpdate({ kind: x.kind, id: x.id, agent: x.agent });
      setD(r.state);
      setState({ addonUpdates: r.state.items.length });
      setDone((m) => ({ ...m, [k]: { name: x.name, text: x.kind === "plugin" && x.latest ? t("aup.doneTo", { v: x.latest }) : t("aup.done") } }));
      // a plugin asking for more waits for 批准
      if (x.kind === "plugin" && r.result && r.result.approved === false) onApprove(r.result as PluginView);
      return true;
    } catch (e: any) {
      setErrs((m) => ({ ...m, [k]: e?.message ?? String(e) }));
      return false;
    } finally {
      setBusy((b) => ({ ...b, [k]: false }));
    }
  };
  const all = async () => {
    const plain = items.filter((x) => !x.newPermissions?.length);
    let ok = 0;
    for (const x of plain) if (await apply(x)) ok++;
    onChanged();
    const held = items.length - plain.length;
    toast(held > 0 ? t("aup.allHeld", { n: ok, held }) : t("aup.allDone", { n: ok }));
  };
  const check = async () => {
    setChecking(true);
    try {
      const x = await API.checkAddonUpdates();
      setD(x);
      setState({ addonUpdates: x.items.length });
      if (!x.items.length) toast(t("aup.none"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setChecking(false);
    }
  };

  if (!d) return null;
  if (!items.length && !doneItems.length)
    return (
      <div className="updquiet small muted">
        <Check size={13} />
        <span>{d.checked ? t("aup.upToDate", { when: new Date(d.checked).toLocaleString() }) : t("aup.notChecked")}</span>
        <button className="linkbtn" type="button" disabled={checking} onClick={() => void check()}>
          {checking ? t("aup.checking") : t("aup.checkNow")}
        </button>
      </div>
    );
  const plainN = items.filter((x) => !x.newPermissions?.length).length;
  return (
    <section className="updsec" aria-label={t("aup.title")}>
      <div className="updhead">
        <Sparkles size={15} />
        <b>{items.length ? t("aup.available", { n: items.length }) : t("aup.allApplied")}</b>
        <span className="sp" />
        <button className="ix" type="button" title={t("aup.checkNow")} aria-label={t("aup.checkNow")} disabled={checking} onClick={() => void check()}>
          <RefreshCw size={13} className={checking ? "spinning" : ""} />
        </button>
        {plainN > 1 && (
          <button className="btn sm pri" type="button" disabled={Object.values(busy).some(Boolean)} onClick={() => void all()}>
            {t("aup.all")}
          </button>
        )}
      </div>
      <div className="updlist">
        {items.map((x) => {
          const k = key(x);
          const perms = x.newPermissions ?? [];
          const notes = x.notes?.trim();
          return (
            <div className="updrow" key={k}>
              <div className="updmain">
                <span className={"updkind k-" + x.kind}>{t(`aup.kind.${x.kind}` as any)}</span>
                <div className="g">
                  <b>{x.name}</b>
                  <span className="small muted">
                    {x.kind === "plugin" && x.current && x.latest ? (
                      <span className="updver">
                        {x.current} <ArrowRight size={11} /> {x.latest}
                      </span>
                    ) : (
                      t(`aup.what.${x.kind}` as any)
                    )}
                    {x.agent ? " · " + t("aup.forAgent") : ""}
                  </span>
                </div>
                {perms.length > 0 ? (
                  <button className="btn sm" type="button" disabled={busy[k]} onClick={() => setOpen(open === k ? null : k)}>
                    <ShieldAlert size={12} />
                    {t("aup.review")}
                  </button>
                ) : (
                  <button className="btn sm pri" type="button" disabled={busy[k]} onClick={() => void apply(x).then((ok) => ok && onChanged())}>
                    {busy[k] ? <span className="spin sm" /> : null}
                    {busy[k] ? t("aup.updating") : t("aup.update")}
                  </button>
                )}
              </div>
              {(notes || perms.length > 0) && (
                <button className="updmore small" type="button" aria-expanded={open === k} onClick={() => setOpen(open === k ? null : k)}>
                  {open === k ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
                  {perms.length > 0 ? t("aup.newPerms", { n: perms.length }) : t("aup.notes")}
                </button>
              )}
              {open === k && (
                <div className="upddetail">
                  {perms.length > 0 && (
                    <div className="updperms">
                      <p className="small">{t("aup.permsWhy")}</p>
                      <ul>
                        {perms.map((p) => (
                          <li key={p} className="mono small">
                            {p}
                          </li>
                        ))}
                      </ul>
                      <button className="btn sm pri" type="button" disabled={busy[k]} onClick={() => void apply(x).then((ok) => ok && onChanged())}>
                        {busy[k] ? t("aup.updating") : t("aup.updateThenApprove")}
                      </button>
                    </div>
                  )}
                  {notes && (
                    <div className="updnotes fxmd">
                      <Markdown text={notes.length > 3000 ? notes.slice(0, 3000) + "\n\n…" : notes} />
                    </div>
                  )}
                </div>
              )}
              {x.kind === "app" && <p className="small muted updhint">{t("aup.appHint")}</p>}
              {errs[k] && <div className="ferr">{t("aup.failed", { error: errs[k] })}</div>}
            </div>
          );
        })}
        {doneItems.map((k) => (
          <div className="updrow done" key={"done:" + k}>
            <Check size={13} />
            <span className="small">
              {done[k].name} · {done[k].text}
            </span>
          </div>
        ))}
      </div>
    </section>
  );
}
