import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronDown, Dices, FileUp, Folder, Plus, RefreshCw, Shield } from "lucide-react";
import { API, refString, type ModelRef } from "../api";
import { ImportRoleDialog } from "./ImportRole";
import { t, type Key } from "../i18n";
import { allModels, effortLabel, modeLabel, modelMeta, sameRef, shortModel } from "../format";
import { openSession, refreshAgents, refreshSessions, setState, toast, useFolders, useStore, currentTeam, teamMode } from "../store";
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
  return { name: "", role: "", desc: "", color: COLORS[0], shape: "sq", mode: teamMode("sandbox"), folder: "", model: null, effort: "medium" };
}

export type TemplateKey =
  | "lead" | "researcher" | "translator" | "weekly" | "review" | "frontend" | "backend" | "tester" | "architect" | "techdocs" | "release" | "uiux" | "designqa" | "uxwriter" | "visual" | "pm" | "userresearch" | "watch" | "data" | "writer" | "social" | "seo" | "community" | "campaign" | "support" | "oncall" | "sre" | "cicd" | "security" | "incident" | "cost";

export type TemplateCat = "general" | "dev" | "design" | "product" | "growth" | "devops";
/** template areas, in the order the picker shows them */
export const TEMPLATE_CATS: TemplateCat[] = ["general", "dev", "design", "product", "growth", "devops"];

/** The template library, grouped by area (开发、设计、产品、运营、运维…);
 *  the welcome page shows three at a time and can shuffle. */
export const TEMPLATES: { key: TemplateKey; cat: TemplateCat; color: string; shape: string; mode: Draft["mode"]; instr?: boolean }[] = [
  // General
  { key: "lead", cat: "general", color: "#3b32c4", shape: "hex", mode: "sandbox", instr: true },
  { key: "researcher", cat: "general", color: "#6c6cf0", shape: "blob", mode: "read-only", instr: true },
  { key: "translator", cat: "general", color: "#1fb5a8", shape: "circle", mode: "sandbox", instr: true },
  { key: "weekly", cat: "general", color: "#f2b33d", shape: "hex", mode: "sandbox", instr: true },
  // Engineering
  { key: "review", cat: "dev", color: "#24b07e", shape: "circle", mode: "sandbox", instr: true },
  { key: "frontend", cat: "dev", color: "#3987e5", shape: "sq", mode: "sandbox", instr: true },
  { key: "backend", cat: "dev", color: "#3987e5", shape: "oct", mode: "sandbox", instr: true },
  { key: "tester", cat: "dev", color: "#ef8a45", shape: "diamond", mode: "sandbox", instr: true },
  { key: "architect", cat: "dev", color: "#6c6cf0", shape: "shield", mode: "read-only", instr: true },
  { key: "techdocs", cat: "dev", color: "#3987e5", shape: "ticket", mode: "sandbox", instr: true },
  { key: "release", cat: "dev", color: "#3a4aa8", shape: "sq", mode: "sandbox", instr: true },
  // Design
  { key: "uiux", cat: "design", color: "#d651c8", shape: "flower", mode: "sandbox", instr: true },
  { key: "designqa", cat: "design", color: "#d651c8", shape: "drop", mode: "read-only", instr: true },
  { key: "uxwriter", cat: "design", color: "#f07aa8", shape: "pill", mode: "sandbox", instr: true },
  { key: "visual", cat: "design", color: "#f07aa8", shape: "spark", mode: "sandbox", instr: true },
  // Product
  { key: "pm", cat: "product", color: "#6c6cf0", shape: "tri", mode: "sandbox", instr: true },
  { key: "userresearch", cat: "product", color: "#24b07e", shape: "blob", mode: "read-only", instr: true },
  { key: "watch", cat: "product", color: "#8b7cf6", shape: "blob", mode: "read-only", instr: true },
  { key: "data", cat: "product", color: "#2db7f5", shape: "hex", mode: "sandbox", instr: true },
  // Growth & ops
  { key: "writer", cat: "growth", color: "#d651c8", shape: "pill", mode: "sandbox", instr: true },
  { key: "social", cat: "growth", color: "#ef8a45", shape: "flower", mode: "sandbox", instr: true },
  { key: "seo", cat: "growth", color: "#24b07e", shape: "tri", mode: "read-only", instr: true },
  { key: "community", cat: "growth", color: "#1fb5a8", shape: "circle", mode: "sandbox", instr: true },
  { key: "campaign", cat: "growth", color: "#f2b33d", shape: "ticket", mode: "sandbox", instr: true },
  { key: "support", cat: "growth", color: "#f07aa8", shape: "pill", mode: "read-only", instr: true },
  // DevOps & SRE
  { key: "oncall", cat: "devops", color: "#e5484d", shape: "tri", mode: "read-only", instr: true },
  { key: "sre", cat: "devops", color: "#b8314f", shape: "shield", mode: "sandbox", instr: true },
  { key: "cicd", cat: "devops", color: "#2db7f5", shape: "oct", mode: "sandbox", instr: true },
  { key: "security", cat: "devops", color: "#b8314f", shape: "shield", mode: "read-only", instr: true },
  { key: "incident", cat: "devops", color: "#b8314f", shape: "drop", mode: "sandbox", instr: true },
  { key: "cost", cat: "devops", color: "#24b07e", shape: "hex", mode: "read-only", instr: true },
];

/** Three templates of an area ("" = all) starting at page*3, wrapping around. */
export function templatePage(page: number, cat: TemplateCat | "" = "") {
  const list = cat ? TEMPLATES.filter((x) => x.cat === cat) : TEMPLATES;
  const n = list.length;
  if (n <= 3) return list;
  const start = ((page * 3) % n + n) % n;
  return [0, 1, 2].map((i) => list[(start + i) % n]);
}

/** "Shuffle" control: 换一批 with a page counter. */
export function ShuffleButton({ page, onNext, cat = "" }: { page: number; onNext: () => void; cat?: TemplateCat | "" }) {
  const pages = Math.ceil((cat ? TEMPLATES.filter((x) => x.cat === cat) : TEMPLATES).length / 3);
  if (pages <= 1) return null;
  return (
    <button className="btn ghost sm shuffle" type="button" onClick={onNext} title={t("na.shuffle")}>
      <RefreshCw size={13} />
      {t("na.shuffle")}
      <span className="muted">
        {(((page % pages) + pages) % pages) + 1}/{pages}
      </span>
    </button>
  );
}

export function NewAgent({ draft: f, setDraft }: { draft: Draft; setDraft: (d: Draft) => void }) {
  const models = useStore((s) => s.models);
  const [err, setErr] = useState(false);
  const [busy, setBusy] = useState(false);
  const nameRef = useRef<HTMLInputElement>(null);
  const modelM = useMenu();
  const [poke, setPoke] = useState(0);
  const folderM = useMenu();
  const [newFolder, setNewFolder] = useState("");
  // which start the form came from ("" = blank, null = edited by hand)
  // a template chosen before this page opened (the welcome page) is the
  // one already in the form: recognise it by its name, colour and shape
  const [picked, setPicked] = useState<string | null>(() => {
    const x = TEMPLATES.find((tp) => tp.color === f.color && tp.shape === f.shape && t(`tpl.${tp.key}.name` as Key) === f.name);
    return x ? x.key : f.name ? null : "";
  });
  const pickTemplate = (x: (typeof TEMPLATES)[number]) => {
    setErr(false);
    setPicked(x.key);
    set({
      name: t(`tpl.${x.key}.name` as Key),
      desc: t(`tpl.${x.key}.${x.instr ? "instr" : "desc"}` as Key),
      role: t(`tpl.${x.key}.role` as Key),
      color: x.color,
      shape: x.shape,
      mode: teamMode(x.mode),
    });
    nameRef.current?.focus();
  };
  const pickBlank = () => {
    setPicked("");
    setDraft({ ...emptyDraft(), folder: f.folder, model: f.model, effort: f.effort });
    nameRef.current?.focus();
  };
  const randomLook = () => {
    const other = <T,>(xs: readonly T[], cur: T) => {
      const rest = xs.filter((x) => x !== cur);
      return rest[Math.floor(Math.random() * rest.length)];
    };
    set({ color: other(COLORS, f.color), shape: other(SHAPES, f.shape as (typeof SHAPES)[number]) });
  };
  const modeM = useMenu();
  const all = useMemo(() => allModels(models), [models]);
  const folders = useFolders();
  // the team on screen may start its agents read-only (teams load after
  // the draft was first made)
  const teamDefault = useStore((s) => s.teams.find((x) => x.id === s.team)?.defaultMode);
  // opened from a folder's + : the agent goes into that folder
  const createIn = useStore((s) => s.createIn);
  useEffect(() => {
    if (createIn) {
      setDraft({ ...f, folder: createIn });
      setState({ createIn: "" });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [createIn]);
  useEffect(() => {
    if (teamDefault === "read-only" && f.mode === "sandbox") setDraft({ ...f, mode: "read-only" });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [teamDefault]);
  // Default: the same preference the daemon uses for new agents (a strong
  // model from what is available), not simply the first listed.
  const settingsDefault = useStore((s) => s.profile.defaultModels?.[0]);
  const preferred = useMemo(() => {
    if (settingsDefault) {
      const hit = all.find((m) => refString(m.ref) === settingsDefault || refString({ ...m.ref, effort: undefined }) === settingsDefault.split("@")[0]);
      if (hit) return hit.ref;
    }
    for (const want of ["claude-opus-5-5", "claude-sonnet-5-5", "gpt-6.1", "gpt-6", "deepseek-v4-pro", "deepseek-flash"]) {
      const hit = all.find((m) => m.ref.model.includes(want));
      if (hit) return hit.ref;
    }
    return all[0]?.ref ?? null;
  }, [all, settingsDefault]);
  const primary = f.model ?? preferred;
  const backup = all.find((m) => primary && !sameRef(m.ref, primary))?.ref ?? null;
  const set = (p: Partial<Draft>) => setDraft({ ...f, ...p });
  const [importing, setImporting] = useState(false);
  const modelRefs = () => {
    const refs: string[] = [];
    if (primary) refs.push(refString({ ...primary, effort: f.effort || undefined }));
    if (backup) refs.push(refString(backup));
    return refs;
  };

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
      const refs = modelRefs();
      const a = await API.createAgent({
        name,
        role: f.role.trim() || undefined,
        instructions: f.desc.trim() || undefined,
        mode: f.mode,
        color: f.color,
        shape: f.shape,
        folder: f.folder.trim() || undefined,
        team: currentTeam(),
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
        <div className="na-grid">
          {/* left: where to start — blank or a template; picking one fills the form */}
          <aside className="na-side">
            <div className="na-sideh">{t("na.startFrom")}</div>
            <button type="button" className={"tpl" + (picked === "" ? " on" : "")} onClick={() => pickBlank()}>
              <span className="tplblank">
                <Plus size={18} />
              </span>
              <span className="g">
                <b>{t("na.blank")}</b>
                <span>{t("na.blankDesc")}</span>
              </span>
            </button>
            {TEMPLATE_CATS.map((c) => (
              <div className="tplgrp" key={c}>
                <div className="tplcat">{t(`tplcat.${c}` as Key)}</div>
                {TEMPLATES.filter((x) => x.cat === c).map((x) => (
                  <button key={x.key} type="button" className={"tpl" + (picked === x.key ? " on" : "")} onClick={() => pickTemplate(x)}>
                    <AvatarRaw color={x.color} shape={x.shape} size={34} name={t(`tpl.${x.key}.name` as Key)} />
                    <span className="g">
                      <b>{t(`tpl.${x.key}.name` as Key)}</b>
                      <span>{t(`tpl.${x.key}.desc` as Key)}</span>
                    </span>
                  </button>
                ))}
              </div>
            ))}
            <button type="button" className="tpl imp" onClick={() => setImporting(true)}>
              <span className="tplblank">
                <FileUp size={16} />
              </span>
              <span className="g">
                <b>{t("ir.title")}</b>
                <span>{t("ir.entryDesc")}</span>
              </span>
            </button>
          </aside>

          {/* right: the agent */}
          <div className="na-main">
            <div className="na-card na-id">
              <button
                type="button"
                className="na-heroav"
                key={f.color + f.shape + poke}
                title={t("na.poke")}
                aria-label={t("na.poke")}
                onClick={() => setPoke((n) => n + 1)}
              >
                <AvatarRaw color={f.color} shape={f.shape} size={84} name={f.name} />
              </button>
              <div className="na-look">
                <div className="na-lookh">
                  <span className="small muted">{t("na.look")}</span>
                  <button type="button" className="linkbtn na-dice" onClick={randomLook}>
                    <Dices size={13} />
                    {t("na.random")}
                  </button>
                </div>
                <div className="na-shapes" role="radiogroup" aria-label={t("na.shape")}>
                  {SHAPES.map((sh) => (
                    <button key={sh} type="button" className="shp" aria-pressed={f.shape === sh} aria-label={sh} onClick={() => set({ shape: sh })}>
                      <AvatarRaw color={f.color} shape={sh} size={26} name={f.name} />
                    </button>
                  ))}
                </div>
                <div className="na-colors" role="radiogroup" aria-label={t("na.color")}>
                  {COLORS.map((c) => (
                    <button key={c} type="button" className="dotc" style={{ ["--c" as any]: c }} aria-pressed={f.color === c} aria-label={c} onClick={() => set({ color: c })} />
                  ))}
                </div>
              </div>
            </div>

            <div className="na-card">
              <div className="na-two">
                <div>
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
                </div>
                <div>
                  <label className="fl" htmlFor="naRole">
                    {t("na.role")}
                  </label>
                  <input id="naRole" className="fi" value={f.role} maxLength={16} placeholder={t("na.rolePh")} onChange={(e) => set({ role: e.target.value })} />
                </div>
              </div>
              <label className="fl" htmlFor="naDesc">
                {t("na.desc")}
              </label>
              <textarea
                id="naDesc"
                className="fi na-desc"
                rows={f.desc.length > 160 ? 8 : 3}
                value={f.desc}
                placeholder={t("na.descPh")}
                onChange={(e) => set({ desc: e.target.value })}
              />
            </div>

            <div className="na-card rows">
              <button type="button" className="rowsel" ref={modelM.ref} onClick={modelM.toggle} aria-haspopup="menu">
                <span>{t("na.model")}</span>
                <span className="v">
                  {primary ? (
                    <>
                      <ProviderMark provider={primary.provider} model={primary.model} />
                      {shortModel(models, primary)}
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
                    <ProviderMark provider={backup.provider} model={backup.model} size={12} />
                    <span className="ml">{t("na.backup", { model: shortModel(models, backup) })}</span>
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
              <button type="button" className="rowsel" ref={folderM.ref} onClick={folderM.toggle} aria-haspopup="menu">
                <span>{t("na.folder")}</span>
                <span className="v">
                  <Folder size={15} />
                  {f.folder.trim() || <i>{t("na.noFolder")}</i>}
                  <ChevronDown size={14} />
                </span>
              </button>
            </div>

            <div className="na-acts">
              <button className="btn" type="button" onClick={() => setState({ view: "chat" })}>
                {t("common.cancel")}
              </button>
              <button className="btn pri" type="button" disabled={busy} onClick={create}>
                {busy ? t("na.creating") : t("na.create")}
              </button>
            </div>
          </div>
        </div>
      </div>
      <Popover anchor={folderM.anchor} open={folderM.open} onClose={folderM.close} width={260}>
        <div className="mlbl">{t("na.folder")}</div>
        <button
          className="mi"
          type="button"
          onClick={() => {
            set({ folder: "" });
            folderM.close();
          }}
        >
          <span className="g">{t("na.noFolder")}</span>
          <span className="ck">{!f.folder.trim() && <Check size={14} />}</span>
        </button>
        {folders.map((x) => (
          <button
            key={x}
            className="mi"
            type="button"
            onClick={() => {
              set({ folder: x });
              folderM.close();
            }}
          >
            <Folder size={15} />
            <span className="g">{x}</span>
            <span className="ck">{f.folder.trim() === x && <Check size={14} />}</span>
          </button>
        ))}
        <hr />
        <form
          className="dirtype"
          onSubmit={(e) => {
            e.preventDefault();
            const v = newFolder.trim();
            if (v) {
              set({ folder: v });
              setNewFolder("");
              folderM.close();
            }
          }}
        >
          <input className="fi" value={newFolder} maxLength={40} onChange={(e) => setNewFolder(e.target.value)} placeholder={t("na.newFolderPh")} />
          <button className="btn sm" type="submit" disabled={!newFolder.trim()}>
            {t("common.add")}
          </button>
        </form>
      </Popover>
      {importing && (
        <ImportRoleDialog
          onClose={() => setImporting(false)}
          options={{ mode: f.mode, color: f.color, shape: f.shape, folder: f.folder.trim() || undefined, modelRefs: modelRefs() }}
        />
      )}

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
            <ProviderMark provider={m.ref.provider} model={m.ref.model} size={16} />
            <span className="g">
              {m.info.name || m.info.id}
              <span>
                {t(`kind.${m.provider.kind}` as Key)}
                {modelMeta(m.info) ? ` · ${modelMeta(m.info)}` : ""}
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
