import { FileUp, FolderUp, Loader2, ShieldCheck, X } from "lucide-react";
import { Popover, lastPressed } from "./Popover";
import { useRef, useState } from "react";
import { API, type ImportOptions, type ImportSource, type RoleTemplate } from "../api";
import { t, type Key } from "../i18n";
import { openSession, refreshAgents, refreshSessions, toast } from "../store";

const MAX_FILE = 1 << 20;
const MAX_TOTAL = 8 << 20;
const NOISE = /(^|\/)(\.git|node_modules|__pycache__|\.cache)\//;

function b64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

/** Reads picked files into an import source. Oversized and obviously
 *  irrelevant files are left out here (and listed), the daemon decides
 *  the rest. */
async function readFiles(list: FileList): Promise<{ src: ImportSource; local: { path: string; reason: string }[] }> {
  const files = [...list];
  const local: { path: string; reason: string }[] = [];
  if (files.length === 1 && /\.zip$/i.test(files[0].name)) {
    if (files[0].size > MAX_TOTAL) throw new Error(t("ir.tooBig"));
    return { src: { zip: b64(await files[0].arrayBuffer()) }, local };
  }
  const out: { path: string; data: string }[] = [];
  let total = 0;
  for (const f of files) {
    const p = (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name;
    if (NOISE.test(p)) continue;
    if (f.size > MAX_FILE) {
      local.push({ path: p, reason: "too-large" });
      continue;
    }
    total += f.size;
    if (total > MAX_TOTAL) throw new Error(t("ir.tooBig"));
    out.push({ path: p, data: b64(await f.arrayBuffer()) });
  }
  return { src: { files: out }, local };
}

const REASONS = ["memory", "conversation", "secret", "settings", "too-large", "unsupported"] as const;

/** 导入角色模板 (C-5): only instructions and skills move over. */
export function ImportRoleDialog({ onClose, options }: { onClose: () => void; options: ImportOptions }) {
  const [anchor] = useState(() => lastPressed());
  const [src, setSrc] = useState<ImportSource | null>(null);
  const [tpl, setTpl] = useState<RoleTemplate | null>(null);
  const [local, setLocal] = useState<{ path: string; reason: string }[]>([]);
  const [text, setText] = useState("");
  const [name, setName] = useState("");
  const [role, setRole] = useState("");
  const [busy, setBusy] = useState<"" | "read" | "import">("");
  const [error, setError] = useState("");
  const dirIn = useRef<HTMLInputElement>(null);
  const fileIn = useRef<HTMLInputElement>(null);

  const preview = async (s: ImportSource, loc: { path: string; reason: string }[] = []) => {
    setBusy("read");
    setError("");
    try {
      const r = await API.previewImport(s);
      setSrc(s);
      setTpl(r);
      setLocal(loc);
      setName(r.name || "");
      setRole(r.role || "");
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  };

  const pick = async (list: FileList | null) => {
    if (!list?.length) return;
    try {
      const { src: s, local: loc } = await readFiles(list);
      await preview(s, loc);
    } catch (e: any) {
      setError(e?.message ?? String(e));
    }
  };

  const doImport = async () => {
    if (!src || !tpl) return;
    if (!name.trim()) {
      setError(t("na.needName"));
      return;
    }
    setBusy("import");
    try {
      const r = await API.importAgent(src, { ...options, name: name.trim(), role: role.trim() });
      toast(r.skills ? t("ir.doneSkills", { name: r.name, n: r.skills }) : t("ir.done", { name: r.name }));
      await Promise.allSettled([refreshAgents(), refreshSessions()]);
      onClose();
      openSession(r.session);
    } catch (e: any) {
      setError(e?.message ?? String(e));
    } finally {
      setBusy("");
    }
  };

  const skipped = tpl ? [...tpl.skipped, ...local] : [];
  const byReason = REASONS.map((r) => ({ r, items: skipped.filter((s) => s.reason === r) })).filter((g) => g.items.length);

  // in place, below 导入角色 (it opened from there)
  return (
    <Popover anchor={anchor} open onClose={onClose} className="keep irin">
      <div role="region" aria-label={t("ir.title")}>
        <div className="mhd">
          <h3>{t("ir.title")}</h3>
          <button className="ib" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={16} />
          </button>
        </div>
        <div className="mbd">
          <p className="ir-note">
            <ShieldCheck size={15} />
            <span>{t("ir.note")}</span>
          </p>

          {!tpl ? (
            <>
              <div className="ir-pick">
                <button type="button" className="ir-src" disabled={busy !== ""} onClick={() => dirIn.current?.click()}>
                  <FolderUp size={20} />
                  <b>{t("ir.folder")}</b>
                  <span>{t("ir.folderDesc")}</span>
                </button>
                <button type="button" className="ir-src" disabled={busy !== ""} onClick={() => fileIn.current?.click()}>
                  <FileUp size={20} />
                  <b>{t("ir.file")}</b>
                  <span>{t("ir.fileDesc")}</span>
                </button>
              </div>
              <input
                ref={dirIn}
                type="file"
                hidden
                multiple
                {...({ webkitdirectory: "", directory: "" } as any)}
                onChange={(e) => {
                  void pick(e.target.files);
                  e.target.value = "";
                }}
              />
              <input
                ref={fileIn}
                type="file"
                hidden
                multiple
                accept=".md,.txt,.json,.zip"
                onChange={(e) => {
                  void pick(e.target.files);
                  e.target.value = "";
                }}
              />
              <label className="fl">
                <span>{t("ir.paste")}</span>
                <textarea className="fi ir-text" value={text} placeholder={t("ir.pastePh")} onChange={(e) => setText(e.target.value)} />
              </label>
              {error && <div className="confirmbar errbar">{error}</div>}
              <div className="mft">
                <button className="btn" type="button" onClick={onClose}>
                  {t("common.cancel")}
                </button>
                <button className="btn pri" type="button" disabled={busy !== "" || !text.trim()} onClick={() => void preview({ text })}>
                  {busy === "read" && <Loader2 size={14} className="spin" />}
                  {t("ir.next")}
                </button>
              </div>
            </>
          ) : (
            <>
              <div className="ir-fmt">
                <span className="pill">{t(`ir.fmt.${tpl.format}` as Key)}</span>
                <span className="small">{t("ir.summary", { skills: tpl.skills.length, skipped: skipped.length })}</span>
              </div>
              <div className="ap-row2">
                <label className="fl">
                  <span>{t("na.name")}</span>
                  <input className="fi sm" value={name} maxLength={24} onChange={(e) => setName(e.target.value)} />
                </label>
                <label className="fl">
                  <span>{t("na.role")}</span>
                  <input className="fi sm" value={role} maxLength={40} onChange={(e) => setRole(e.target.value)} />
                </label>
              </div>

              <div className="ir-sec">{t("ir.instructions")}</div>
              <pre className="ir-instr">{tpl.instructions || t("ir.noInstr")}</pre>

              {tpl.skills.length > 0 && (
                <>
                  <div className="ir-sec">{t("ir.skills", { n: tpl.skills.length })}</div>
                  <ul className="ir-list">
                    {tpl.skills.map((s) => (
                      <li key={s.name}>
                        <b className="mono">{s.name}</b>
                        <span>{s.description || t("ir.files", { n: s.files.length })}</span>
                      </li>
                    ))}
                  </ul>
                </>
              )}

              {byReason.length > 0 && (
                <details className="ir-skip">
                  <summary>{t("ir.skipped", { n: skipped.length })}</summary>
                  {byReason.map((g) => (
                    <div key={g.r} className="ir-skg">
                      <b>{t(`ir.why.${g.r}` as Key)}</b>
                      <span className="mono">
                        {g.items
                          .slice(0, 8)
                          .map((s) => s.path)
                          .join("、")}
                        {g.items.length > 8 ? ` +${g.items.length - 8}` : ""}
                      </span>
                    </div>
                  ))}
                </details>
              )}

              {error && <div className="confirmbar errbar">{error}</div>}
              <div className="mft">
                <button
                  className="btn"
                  type="button"
                  onClick={() => {
                    setTpl(null);
                    setSrc(null);
                    setError("");
                  }}
                >
                  {t("ap.back")}
                </button>
                <button className="btn pri" type="button" disabled={busy !== ""} onClick={() => void doImport()}>
                  {busy === "import" && <Loader2 size={14} className="spin" />}
                  {t("ir.import")}
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </Popover>
  );
}
