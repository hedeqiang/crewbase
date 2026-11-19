import { useEffect, useState } from "react";
import { AppWindow, ChevronRight, Download, FileText, Folder, FolderOpen, Pencil, RefreshCw, Save, X } from "lucide-react";
import { API, rawURL, useRawURL, type AgentView, type FileContent, type FileEntry } from "../api";
import { t } from "../i18n";
import { ago } from "../format";
import { setState, toast, useStore } from "../store";
import { Markdown } from "./Markdown";
import { shortPath } from "./Conversation";
import { confirmDialog } from "./Confirm";
import { previewPath } from "./Preview";

function sizeFmt(n: number): string {
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1).replace(/\.0$/, "") + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

/** 文件 tab: workspace tree + preview, edit & save text files. */
export function FilesTab({ agent }: { agent: AgentView }) {
  const want = useStore((s) => s.fileOpen);
  const [sel, setSel] = useState<string | null>(want ?? "INSTRUCTIONS.md");
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (want) {
      setSel(want);
      setState({ fileOpen: null });
    }
  }, [want]);

  return (
    <div className="files">
      <div className="psec fhead">
        <span className="mono" title={agent.workspace}>
          {shortPath(agent.workspace)}
        </span>
        <button className="ix" type="button" aria-label={t("files.refresh")} title={t("files.refresh")} onClick={() => setTick((x) => x + 1)}>
          <RefreshCw size={12} />
        </button>
      </div>
      <div className="tree ftree">
        <Dir key={agent.id + tick} agentId={agent.id} path="" depth={0} sel={sel} onPick={setSel} />
      </div>
      {sel && <Preview key={agent.id + sel + tick} agentId={agent.id} path={sel} onClose={() => setSel(null)} />}
    </div>
  );
}

function Dir({ agentId, path, depth, sel, onPick }: { agentId: string; path: string; depth: number; sel: string | null; onPick: (p: string) => void }) {
  const [entries, setEntries] = useState<FileEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  useEffect(() => {
    API.files(agentId, path)
      .then((r) => setEntries(r.entries ?? []))
      .catch((e) => setErr(e?.message ?? String(e)));
  }, [agentId, path]);
  if (err) return <div className="small errt" style={{ paddingLeft: 8 + depth * 14 }}>{err}</div>;
  if (!entries) return <div className="small" style={{ paddingLeft: 8 + depth * 14 }}>{t("common.loading")}</div>;
  if (entries.length === 0 && depth === 0) return <div className="small">{t("files.empty")}</div>;
  return (
    <>
      {entries.map((e) => {
        const p = path ? path + "/" + e.name : e.name;
        if (e.dir)
          return (
            <div key={p}>
              <div className={"tnrow" + (e.index || e.project ? " pv" : "")}>
                <button className="tn" type="button" style={{ paddingLeft: 6 + depth * 14 }} aria-expanded={!!open[p]} onClick={() => setOpen((o) => ({ ...o, [p]: !o[p] }))}>
                  <ChevronRight size={12} className="tch" />
                  {open[p] ? <FolderOpen size={14} /> : <Folder size={14} />}
                  <span className="g">{e.name}</span>
                </button>
                {/* a project's source runs through its dev server, not as files */}
                {e.project ? <RunPreviewBtn agentId={agentId} path={p} /> : e.index && <PreviewBtn agentId={agentId} path={p + "/"} />}
              </div>
              {open[p] && <Dir agentId={agentId} path={p} depth={depth + 1} sel={sel} onPick={onPick} />}
            </div>
          );
        const html = HTML_RE.test(e.name);
        return (
          <div key={p} className={"tnrow" + (html ? " pv" : "")}>
            <button className="tn" type="button" aria-current={sel === p} style={{ paddingLeft: 24 + depth * 14 }} onClick={() => onPick(p)} title={`${sizeFmt(e.size)} · ${ago(e.modified)}`}>
              <FileText size={14} />
              <span className="g">{e.name}</span>
              <span className="small">{sizeFmt(e.size)}</span>
            </button>
            {html && <PreviewBtn agentId={agentId} path={p} />}
          </div>
        );
      })}
    </>
  );
}

const HTML_RE = /\.html?$/i;

/** 预览: opens an HTML file or a folder with index.html in the 预览 tab. */
function PreviewBtn({ agentId, path }: { agentId: string; path: string }) {
  return (
    <button className="tnpv" type="button" title={t("preview.inPanel")} onClick={() => void previewPath(agentId, path)}>
      <AppWindow size={12} />
      {t("preview.btn")}
    </button>
  );
}

/** 运行并预览: asks the agent to start the project's dev server in the
 *  background and open it in the 预览 tab (its turn shows every step). */
function RunPreviewBtn({ agentId, path }: { agentId: string; path: string }) {
  const agent = useStore((s) => s.agents[agentId]);
  const run = async () => {
    if (!agent) return;
    try {
      await API.send(agentId, { text: t("preview.runAsk", { dir: path }), session: agent.session });
      toast(t("preview.runSent", { name: agent.name }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <button className="tnpv" type="button" title={t("preview.runTitle")} disabled={!agent || agent.kind === "chat"} onClick={() => void run()}>
      <AppWindow size={12} />
      {t("preview.run")}
    </button>
  );
}

function Preview({ agentId, path, onClose }: { agentId: string; path: string; onClose: () => void }) {
  const [f, setF] = useState<FileContent | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    API.file(agentId, path)
      .then(setF)
      .catch((e) => setErr(e?.status === 404 ? t("files.missing") : e?.message ?? String(e)));
  }, [agentId, path]);

  const image = /\.(png|jpe?g|gif|webp)$/i.test(path);
  const video = /\.(mp4|webm)$/i.test(path);
  // documents may look like text (an ASCII PDF) but must never be edited as text
  const doc = /\.(pdf|docx?|xlsx?|pptx?|zip|gz|tar|heic|bmp|tiff?|svg)$/i.test(path);
  const text = !!f && !f.binary && !f.tooLarge && !doc && !image && !video;
  const dirty = edit !== null && edit !== (f?.content ?? "");
  const save = async () => {
    if (edit === null) return;
    setSaving(true);
    try {
      await API.saveFile(agentId, path, edit);
      setF({ path, size: new Blob([edit]).size, content: edit });
      setEdit(null);
      toast(t("files.saved", { name: path }));
    } catch (e: any) {
      toast(t("files.saveFailed", { error: e?.message ?? String(e) }));
    } finally {
      setSaving(false);
    }
  };
  // A missing INSTRUCTIONS.md / MEMORY.md can be created from here.
  const creatable = !!err && !f && /^(INSTRUCTIONS|MEMORY)\.md$/.test(path);

  return (
    <div className="fprev">
      <div className="fph">
        <FileText size={14} />
        <b className="mono">{path}</b>
        {f && <span className="small">{sizeFmt(f.size)}</span>}
        {dirty && <span className="pill amber">{t("files.unsaved")}</span>}
        <span style={{ flex: 1 }} />
        {edit === null ? (
          (text || creatable) && (
            <button className="ix" type="button" aria-label={t("files.edit")} title={t("files.edit")} onClick={() => setEdit(f?.content ?? "")}>
              <Pencil size={13} />
            </button>
          )
        ) : (
          <>
            <button className="btn pri sm" type="button" disabled={saving || !dirty} onClick={save}>
              <Save size={12} />
              {t("common.save")}
            </button>
            <button className="btn ghost sm" type="button" onClick={async () => (!dirty || (await confirmDialog(t("files.discard")))) && setEdit(null)}>
              {t("common.cancel")}
            </button>
          </>
        )}
        {edit === null && HTML_RE.test(path) && <PreviewBtn agentId={agentId} path={path} />}
        {edit === null && f && (
          <a className="ix" href={rawURL(agentId, path)} download={path.split("/").pop()} aria-label={t("files.download")} title={t("files.download")}>
            <Download size={13} />
          </a>
        )}
        {edit === null && (
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={13} />
          </button>
        )}
      </div>
      {edit !== null ? (
        <textarea
          className="fedit mono"
          value={edit}
          autoFocus
          spellCheck={false}
          onChange={(e) => setEdit(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
              e.preventDefault();
              void save();
            }
          }}
        />
      ) : err ? (
        <div className="small" style={{ padding: 10 }}>
          {creatable ? t(path.startsWith("MEMORY") ? "files.noMemory" : "files.noInstructions") : err}
        </div>
      ) : !f ? (
        <div className="small" style={{ padding: 10 }}>
          {t("common.loading")}
        </div>
      ) : image ? (
        <div className="fbody fimg">
          <RawImg agent={agentId} path={path} />
        </div>
      ) : video ? (
        <div className="fbody fimg">
          <RawVideo agent={agentId} path={path} />
        </div>
      ) : f.tooLarge ? (
        <div className="small" style={{ padding: 10 }}>
          {t("files.tooLarge")}
        </div>
      ) : f.binary || doc ? (
        <div className="fbin">
          <span className="small">{t("files.binary")}</span>
          <a className="btn sm" href={rawURL(agentId, path)} download={path.split("/").pop()}>
            <Download size={12} />
            {t("files.download")}
          </a>
        </div>
      ) : /\.md$/i.test(path) && (f.content ?? "").trim() ? (
        <div className="fbody mt">
          <Markdown text={f.content ?? ""} />
        </div>
      ) : (
        <pre className="fbody code">{f.content || t("files.emptyFile")}</pre>
      )}
    </div>
  );
}

function RawVideo({ agent, path }: { agent: string; path: string }) {
  const src = useRawURL(agent, path);
  return src ? <video src={src} controls preload="metadata" playsInline /> : <span className="small">{t("common.loading")}</span>;
}

function RawImg({ agent, path }: { agent: string; path: string }) {
  const src = useRawURL(agent, path);
  return src ? <img src={src} alt={path} /> : <span className="small">{t("common.loading")}</span>;
}
