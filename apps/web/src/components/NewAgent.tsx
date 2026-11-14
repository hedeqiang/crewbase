import { useMemo, useRef, useState } from "react";
import { Check, ChevronDown, Folder, Shield } from "lucide-react";
import { API, refString, type ModelRef } from "../api";
import { t, type Key } from "../i18n";
import { allModels, effortLabel, kfmt, modeLabel, modelLabel, sameRef } from "../format";
import { openSession, refreshAgents, refreshSessions, setState, toast, useStore } from "../store";
import { AvatarRaw, COLORS, SHAPES } from "./Avatar";
import { Popover, useMenu } from "./Popover";
import { ProviderMark } from "./ProviderMark";

export interface Draft {
  name: string;
  role: string;
  desc: string;
  color: string;
  shape: string;
  mode: "read-only" | "sandbox";
  folder: string;
  model: ModelRef | null;
  effort: string;
}

export function emptyDraft(): Draft {
  return { name: "", role: "", desc: "", color: COLORS[0], shape: "sq", mode: "sandbox", folder: "", model: null, effort: "medium" };
}

const TEMPLATES: { key: "review" | "watch" | "weekly"; color: string; shape: string; mode: Draft["mode"] }[] = [
  { key: "review", color: "#24b07e", shape: "circle", mode: "sandbox" },
  { key: "watch", color: "#8b7cf6", shape: "blob", mode: "read-only" },
  { key: "weekly", color: "#f2b33d", shape: "hex", mode: "sandbox" },
];

export function NewAgent({ draft: f, setDraft }: { draft: Draft; setDraft: (d: Draft) => void }) {
  const models = useStore((s) => s.models);
  const agents = useStore((s) => s.agents);
  const [err, setErr] = useState(false);
  const [busy, setBusy] = useState(false);
  const nameRef = useRef<HTMLInputElement>(null);
  const modelM = useMenu();
  const modeM = useMenu();
  const all = useMemo(() => allModels(models), [models]);
  const folders = useMemo(() => [...new Set(Object.values(agents).map((a) => a.folder).filter(Boolean) as string[])].sort(), [agents]);
  const primary = f.model ?? all[0]?.ref ?? null;
  const backup = all.find((m) => primary && !sameRef(m.ref, primary))?.ref ?? null;
  const set = (p: Partial<Draft>) => setDraft({ ...f, ...p });

  const create = async () => {
    const name = f.name.trim();
    if (!name) {
      setErr(true);
      toast(t("na.needName"));
      nameRef.current?.focus();
      return;
    }
    setBusy(true);
    try {
      const refs: string[] = [];
      if (primary) refs.push(refString({ ...primary, effort: f.effort || undefined }));
      if (backup) refs.push(refString(backup));
      const a = await API.createAgent({
        name,
        role: f.role.trim() || undefined,
        instructions: f.desc.trim() || undefined,
        mode: f.mode,
        color: f.color,
        shape: f.shape,
        folder: f.folder.trim() || undefined,
        modelRefs: refs.length ? refs : undefined,
      });
      toast(t("na.created", { name }));
      setDraft(emptyDraft());
      await Promise.allSettled([refreshAgents(), refreshSessions()]);
      openSession(a.session);
    } catch (e: any) {
      toast(t("na.failed", { error: e?.message ?? String(e) }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="main">
      <div className="na">
        <div className="na-col">
          <div className="na-hero">
            <AvatarRaw color={f.color} shape={f.shape} size={92} />
            <b className="na-prev">{f.name.trim() || t("na.newAgent")}</b>
          </div>
          <div className="na-colors" role="radiogroup" aria-label={t("na.color")}>
            {COLORS.map((c) => (
              <button key={c} type="button" className="dotc" style={{ ["--c" as any]: c }} aria-pressed={f.color === c} aria-label={c} onClick={() => set({ color: c })} />
            ))}
          </div>
          <div className="na-shapes" role="radiogroup" aria-label={t("na.shape")}>
            {SHAPES.map((s) => (
              <button key={s} type="button" className="shp" aria-pressed={f.shape === s} aria-label={s} onClick={() => set({ shape: s })}>
                <AvatarRaw color={f.color} shape={s} size={30} />
              </button>
            ))}
          </div>

          <label className="fl" htmlFor="naName">
            {t("na.name")} <i>*</i>
          </label>
          <input
            ref={nameRef}
            id="naName"
            className={"fi" + (err ? " bad" : "")}
            value={f.name}
            maxLength={24}
            autoComplete="off"
            placeholder={t("na.namePh")}
            onChange={(e) => {
              set({ name: e.target.value });
              if (e.target.value.trim()) setErr(false);
            }}
            onKeyDown={(e) => e.key === "Enter" && create()}
          />
          {err && <div className="ferr">{t("na.needName")}</div>}

          <label className="fl" htmlFor="naRole">
            {t("na.role")}
          </label>
          <input id="naRole" className="fi" value={f.role} maxLength={16} placeholder={t("na.rolePh")} onChange={(e) => set({ role: e.target.value })} />

          <label className="fl" htmlFor="naDesc">
            {t("na.desc")}
          </label>
          <textarea id="naDesc" className="fi" rows={3} value={f.desc} placeholder={t("na.descPh")} onChange={(e) => set({ desc: e.target.value })} />

          <div className="rows">
            <button type="button" className="rowsel" ref={modelM.ref} onClick={modelM.toggle} aria-haspopup="menu">
              <span>{t("na.model")}</span>
              <span className="v">
                {primary ? (
                  <>
                    <ProviderMark provider={primary.provider} />
                    {modelLabel(models, primary)}
                    <i>· {effortLabel(f.effort) || t("effort.default")}</i>
                  </>
                ) : (
                  <i>{t("na.modelNone")}</i>
                )}
                <ChevronDown size={14} />
              </span>
            </button>
            <div className="rowhint">
              {backup ? (
                <>
                  <ProviderMark provider={backup.provider} size={12} />
                  {t("na.backup", { model: modelLabel(models, backup) })}
                </>
              ) : (
                t("na.noBackup")
              )}
            </div>
            <button type="button" className="rowsel" ref={modeM.ref} onClick={modeM.toggle} aria-haspopup="menu">
              <span>{t("na.mode")}</span>
              <span className="v">
                <Shield size={15} />
                {modeLabel(f.mode)}
                <ChevronDown size={14} />
              </span>
            </button>
            <label className="rowsel">
              <span>{t("na.folder")}</span>
              <span className="v">
                <Folder size={15} />
                <input className="inl" list="naFolders" value={f.folder} placeholder={t("na.folderPh")} onChange={(e) => set({ folder: e.target.value })} />
                <datalist id="naFolders">
                  {folders.map((x) => (
                    <option key={x} value={x} />
                  ))}
                </datalist>
              </span>
            </label>
          </div>
          <div className="na-acts">
            <button className="btn ghost" type="button" onClick={() => setState({ view: "chat" })}>
              {t("common.cancel")}
            </button>
            <button className="btn pri big" type="button" disabled={busy} onClick={create}>
              {busy ? t("na.creating") : t("na.create")}
            </button>
          </div>
        </div>

        <div className="na-sug">
          <div className="vsec">{t("na.templates")}</div>
          <div className="sugs">
            {TEMPLATES.map((x) => (
              <button
                key={x.key}
                type="button"
                className="sug"
                onClick={() => {
                  setErr(false);
                  set({
                    name: t(`tpl.${x.key}.name` as Key),
                    desc: t(`tpl.${x.key}.desc` as Key),
                    role: t(`tpl.${x.key}.role` as Key),
                    color: x.color,
                    shape: x.shape,
                    mode: x.mode,
                  });
                }}
              >
                <AvatarRaw color={x.color} shape={x.shape} size={44} />
                <span>
                  <b>{t(`tpl.${x.key}.name` as Key)}</b>
                  <span>{t(`tpl.${x.key}.desc` as Key)}</span>
                  <span className="meta">
                    {primary && <ProviderMark provider={primary.provider} size={12} />}
                    {primary ? modelLabel(models, primary) : ""} · {modeLabel(x.mode)}
                  </span>
                </span>
              </button>
            ))}
          </div>
        </div>
      </div>

      <Popover anchor={modelM.anchor} open={modelM.open} onClose={modelM.close} width={340}>
        <div className="mlbl">{t("na.model")}</div>
        {all.length === 0 && <p className="small" style={{ margin: "4px 9px 8px" }}>{t("na.modelNone")}</p>}
        {all.map((m) => (
          <button
            key={refString(m.ref)}
            className="mi"
            type="button"
            onClick={() => {
              set({ model: m.ref });
              modelM.close();
            }}
          >
            <ProviderMark provider={m.ref.provider} size={16} />
            <span className="g">
              {m.info.name || m.info.id}
              <span>
                {t(`kind.${m.provider.kind}` as Key)}
                {m.info.contextWindow ? ` · ${kfmt(m.info.contextWindow)}` : ""}
              </span>
            </span>
            <span className="ck">{sameRef(primary, m.ref) && <Check size={14} />}</span>
          </button>
        ))}
        <hr />
        <div className="mlbl">{t("comp.effort")}</div>
        <div className="seg">
          {(["low", "medium", "high"] as const).map((e) => (
            <button key={e} type="button" aria-pressed={f.effort === e} onClick={() => set({ effort: e })}>
              {effortLabel(e)}
            </button>
          ))}
        </div>
      </Popover>
      <Popover anchor={modeM.anchor} open={modeM.open} onClose={modeM.close} width={320}>
        <div className="mlbl">{t("na.mode")}</div>
        {(["read-only", "sandbox"] as const).map((m) => (
          <button
            key={m}
            className="mi"
            type="button"
            onClick={() => {
              set({ mode: m });
              modeM.close();
            }}
          >
            <Shield size={16} />
            <span className="g">
              {modeLabel(m)}
              <span>{t(`mode.${m}.desc` as Key)}</span>
            </span>
            <span className="ck">{f.mode === m && <Check size={14} />}</span>
          </button>
        ))}
        <p className="small" style={{ margin: "4px 9px 6px" }}>
          {t("na.modeNote")}
        </p>
      </Popover>
    </section>
  );
}
