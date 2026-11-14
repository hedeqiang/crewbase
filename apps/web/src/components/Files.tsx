import { useEffect, useState } from "react";
import { ChevronRight, FileText, Folder, FolderOpen, Pencil, RefreshCw, Save, X } from "lucide-react";
import { API, type AgentView, type FileContent, type FileEntry } from "../api";
import { t } from "../i18n";
import { ago } from "../format";
import { setState, toast, useStore } from "../store";
import { Markdown } from "./Markdown";
import { shortPath } from "./Conversation";

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
              <button className="tn" type="button" style={{ paddingLeft: 6 + depth * 14 }} aria-expanded={!!open[p]} onClick={() => setOpen((o) => ({ ...o, [p]: !o[p] }))}>
                <ChevronRight size={12} className="tch" />
                {open[p] ? <FolderOpen size={14} /> : <Folder size={14} />}
                <span className="g">{e.name}</span>
              </button>
              {open[p] && <Dir agentId={agentId} path={p} depth={depth + 1} sel={sel} onPick={onPick} />}
            </div>
          );
        return (
          <button key={p} className="tn" type="button" aria-current={sel === p} style={{ paddingLeft: 24 + depth * 14 }} onClick={() => onPick(p)} title={`${sizeFmt(e.size)} · ${ago(e.modified)}`}>
            <FileText size={14} />
            <span className="g">{e.name}</span>
            <span className="small">{sizeFmt(e.size)}</span>
          </button>
        );
      })}
    </>
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

  const text = !!f && !f.binary && !f.tooLarge;
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
            <button className="btn ghost sm" type="button" onClick={() => (!dirty || window.confirm(t("files.discard"))) && setEdit(null)}>
              {t("common.cancel")}
            </button>
          </>
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
          {err}
        </div>
      ) : !f ? (
        <div className="small" style={{ padding: 10 }}>
          {t("common.loading")}
        </div>
      ) : f.tooLarge ? (
        <div className="small" style={{ padding: 10 }}>
          {t("files.tooLarge")}
        </div>
      ) : f.binary ? (
        <div className="small" style={{ padding: 10 }}>
          {t("files.binary")}
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
