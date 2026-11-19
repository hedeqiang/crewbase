import { Fragment, useEffect, useMemo, useState } from "react";
import {
  AppWindow, ChevronDown, ChevronRight, Columns2, Download, File, FileCode2, FileImage, FileJson, FileText, Folder, FolderOpen, GitBranch,
  Pencil, RefreshCw, Rows3, Save, Undo2, X,
} from "lucide-react";
import { API, rawURL, useRawURL, type AgentView, type ChangesView, type FileChange, type FileContent, type FileDiff, type FileEntry, type Hunk } from "../api";
import { t } from "../i18n";
import { ago } from "../format";
import { setState, toast, useStore } from "../store";
import { Markdown } from "./Markdown";
import { shortPath } from "./Conversation";
import { confirmDialog } from "./Confirm";
import { previewPath } from "./Preview";
import { highlightLines, langOf } from "../lib/highlight";

function sizeFmt(n: number): string {
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1).replace(/\.0$/, "") + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

const HTML_RE = /\.html?$/i;
const IMAGE_RE = /\.(png|jpe?g|gif|webp|svg|ico|bmp)$/i;
const VIDEO_RE = /\.(mp4|webm|mov)$/i;
const DOC_RE = /\.(pdf|docx?|xlsx?|pptx?|zip|gz|tar|heic|bmp|tiff?)$/i;

/** A file's icon by its kind. */
function FileIcon({ name, size = 14 }: { name: string; size?: number }) {
  if (IMAGE_RE.test(name) || VIDEO_RE.test(name)) return <FileImage size={size} className="fic img" />;
  if (/\.(json|ya?ml|toml|lock)$/i.test(name)) return <FileJson size={size} className="fic data" />;
  if (/\.(md|txt|rst)$/i.test(name)) return <FileText size={size} className="fic doc" />;
  if (langOf(name)) return <FileCode2 size={size} className="fic code" />;
  return <File size={size} className="fic" />;
}

type View = "files" | "changes";
const VIEW_KEY = "crewbase.files.view";
const LAYOUT_KEY = "crewbase.diff.layout";
const read = (k: string, d: string) => {
  try {
    return localStorage.getItem(k) ?? d;
  } catch {
    return d;
  }
};
const write = (k: string, v: string) => {
  try {
    localStorage.setItem(k, v);
  } catch {
    /* private window */
  }
};

/** 文件 tab: 文件 (the workspace tree and a file) and 改动 (what changed,
 *  as a diff). */
export function FilesTab({ agent }: { agent: AgentView }) {
  const want = useStore((s) => s.fileOpen);
  const [view, setView] = useState<View>(() => (read(VIEW_KEY, "files") === "changes" ? "changes" : "files"));
  const [sel, setSel] = useState<string | null>(want ?? null);
  const [tick, setTick] = useState(0);
  const [changes, setChanges] = useState<ChangesView | null>(null);
  // the conversation's events: changes are looked at again as the agent works
  const evs = useStore((s) => (s.events[agent.session] ?? []).length);

  useEffect(() => {
    if (want) {
      setSel(want);
      setView("files");
      setState({ fileOpen: null });
    }
  }, [want]);
  useEffect(() => write(VIEW_KEY, view), [view]);
  useEffect(() => {
    let live = true;
    API.changes(agent.id).then(
      (c) => live && setChanges(c),
      () => live && setChanges(null),
    );
    return () => {
      live = false;
    };
  }, [agent.id, tick, Math.floor(evs / 3)]);

  const n = changes?.files.length ?? 0;
  return (
    <div className="fx">
      <div className="fxbar">
        <div className="seg fxseg" role="tablist">
          <button type="button" role="tab" aria-selected={view === "files"} aria-pressed={view === "files"} onClick={() => setView("files")}>
            {t("fx.files")}
          </button>
          <button type="button" role="tab" aria-selected={view === "changes"} aria-pressed={view === "changes"} onClick={() => setView("changes")}>
            {t("fx.changes")}
            {n > 0 && <span className="fxn">{n}</span>}
          </button>
        </div>
        <span className="fxpath mono" title={agent.workspace}>
          {changes?.branch && (
            <span className="fxbranch">
              <GitBranch size={12} />
              {changes.branch}
            </span>
          )}
          {shortPath(agent.workspace)}
        </span>
        <button className="ix" type="button" aria-label={t("files.refresh")} title={t("files.refresh")} onClick={() => setTick((x) => x + 1)}>
          <RefreshCw size={13} />
        </button>
      </div>
      {view === "files" ? (
        <div className="fxfiles">
          <div className="fxtree">
            <Dir key={agent.id + tick} agentId={agent.id} path="" depth={0} sel={sel} onPick={setSel} changed={changes} />
          </div>
          {sel ? (
            <Preview key={agent.id + sel + tick} agentId={agent.id} path={sel} onClose={() => setSel(null)} change={changes?.files.find((f) => f.path === sel)} onDiff={() => setView("changes")} />
          ) : (
            <div className="fxempty small muted">{t("fx.pick")}</div>
          )}
        </div>
      ) : (
        <Changes agent={agent} data={changes} onChanged={(c) => setChanges(c)} onOpen={(p) => (setSel(p), setView("files"))} />
      )}
    </div>
  );
}

// ---------------------------------------------------------------- tree

function Dir({ agentId, path, depth, sel, onPick, changed }: { agentId: string; path: string; depth: number; sel: string | null; onPick: (p: string) => void; changed: ChangesView | null }) {
  const [entries, setEntries] = useState<FileEntry[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  useEffect(() => {
    API.files(agentId, path)
      .then((r) => setEntries((r.entries ?? []).slice().sort((a, b) => (a.dir === b.dir ? a.name.localeCompare(b.name) : a.dir ? -1 : 1))))
      .catch((e) => setErr(e?.message ?? String(e)));
  }, [agentId, path]);
  const status = useMemo(() => {
    const m = new Map<string, string>();
    for (const f of changed?.files ?? []) {
      m.set(f.path, f.status);
      // a folder holding changes shows a dot
      const parts = f.path.split("/");
      for (let i = 1; i < parts.length; i++) m.set(parts.slice(0, i).join("/") + "/", m.get(parts.slice(0, i).join("/") + "/") ?? "dir");
    }
    return m;
  }, [changed]);
  const pad = (d: number) => ({ paddingLeft: 8 + d * 14 });
  if (err) return <div className="small errt" style={pad(depth)}>{err}</div>;
  if (!entries) return <div className="small muted fxload" style={pad(depth)}>{t("common.loading")}</div>;
  if (entries.length === 0 && depth === 0) return <div className="small muted fxload">{t("files.empty")}</div>;
  return (
    <>
      {entries.map((e) => {
        const p = path ? path + "/" + e.name : e.name;
        if (e.dir) {
          const st = status.get(p + "/");
          return (
            <Fragment key={p}>
              <div className={"fxrow" + (e.index || e.project ? " pv" : "")}>
                <button className="fxn2" type="button" style={pad(depth)} aria-expanded={!!open[p]} onClick={() => setOpen((o) => ({ ...o, [p]: !o[p] }))}>
                  <ChevronRight size={12} className="fxch" />
                  {open[p] ? <FolderOpen size={14} className="fic dir" /> : <Folder size={14} className="fic dir" />}
                  <span className="g">{e.name}</span>
                  {st && <i className="fxdot" />}
                </button>
                {e.project ? <RunPreviewBtn agentId={agentId} path={p} /> : e.index && <PreviewBtn agentId={agentId} path={p + "/"} />}
              </div>
              {open[p] && <Dir agentId={agentId} path={p} depth={depth + 1} sel={sel} onPick={onPick} changed={changed} />}
            </Fragment>
          );
        }
        const st = status.get(p);
        return (
          <div key={p} className={"fxrow" + (HTML_RE.test(e.name) ? " pv" : "")}>
            <button className="fxn2" type="button" aria-current={sel === p} style={{ paddingLeft: 8 + depth * 14 + 18 }} onClick={() => onPick(p)} title={`${sizeFmt(e.size)} · ${ago(e.modified)}`}>
              <FileIcon name={e.name} />
              <span className={"g" + (st ? " st-" + st : "")}>{e.name}</span>
              {st ? <span className={"fxst st-" + st}>{STATUS_MARK[st] ?? ""}</span> : <span className="fxsz">{sizeFmt(e.size)}</span>}
            </button>
            {HTML_RE.test(e.name) && <PreviewBtn agentId={agentId} path={p} />}
          </div>
        );
      })}
    </>
  );
}

const STATUS_MARK: Record<string, string> = { added: "A", modified: "M", deleted: "D", renamed: "R" };

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

// ---------------------------------------------------------------- one file

function Preview({ agentId, path, onClose, change, onDiff }: { agentId: string; path: string; onClose: () => void; change?: FileChange; onDiff: () => void }) {
  const [f, setF] = useState<FileContent | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [edit, setEdit] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [rendered, setRendered] = useState(true); // Markdown: rendered or source
  useEffect(() => {
    API.file(agentId, path)
      .then(setF)
      .catch((e) => setErr(e?.status === 404 ? t("files.missing") : e?.message ?? String(e)));
  }, [agentId, path]);

  const image = IMAGE_RE.test(path) && !/\.svg$/i.test(path);
  const video = VIDEO_RE.test(path);
  const doc = DOC_RE.test(path);
  const text = !!f && !f.binary && !f.tooLarge && !doc && !image && !video;
  const md = /\.md$/i.test(path);
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
  const creatable = !!err && !f && /^(INSTRUCTIONS|MEMORY)\.md$/.test(path);
  const name = path.split("/").pop() ?? path;
  const dir = path.includes("/") ? path.slice(0, path.lastIndexOf("/") + 1) : "";

  return (
    <div className="fxview">
      <div className="fxhead">
        <FileIcon name={name} />
        <span className="fxtitle" title={path}>
          {dir && <span className="muted">{dir}</span>}
          <b>{name}</b>
        </span>
        {f && <span className="fxmeta">{sizeFmt(f.size)}</span>}
        {change && (
          <button className={"fxchip st-" + change.status} type="button" onClick={onDiff} title={t("fx.seeDiff")}>
            {t(`fx.st.${change.status}` as any)}
            {!change.binary && !change.large && (
              <>
                <span className="add">+{change.added}</span>
                <span className="del">−{change.removed}</span>
              </>
            )}
          </button>
        )}
        {dirty && <span className="pill amber">{t("files.unsaved")}</span>}
        <span className="sp" />
        {edit === null && text && md && (f?.content ?? "").trim() && (
          <span className="seg fxmini">
            <button type="button" aria-pressed={rendered} onClick={() => setRendered(true)}>
              {t("fx.rendered")}
            </button>
            <button type="button" aria-pressed={!rendered} onClick={() => setRendered(false)}>
              {t("fx.source")}
            </button>
          </span>
        )}
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
          <a className="ix" href={rawURL(agentId, path)} download={name} aria-label={t("files.download")} title={t("files.download")}>
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
          className="fxedit mono"
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
        <div className="fxmsg small">{creatable ? t(path.startsWith("MEMORY") ? "files.noMemory" : "files.noInstructions") : err}</div>
      ) : !f ? (
        <div className="fxmsg small muted">{t("common.loading")}</div>
      ) : image ? (
        <div className="fxbody fximg">
          <RawImg agent={agentId} path={path} />
        </div>
      ) : video ? (
        <div className="fxbody fximg">
          <RawVideo agent={agentId} path={path} />
        </div>
      ) : f.tooLarge ? (
        <div className="fxmsg small">{t("files.tooLarge")}</div>
      ) : f.binary || doc ? (
        <div className="fxmsg fxbin">
          <span className="small">{t("files.binary")}</span>
          <a className="btn sm" href={rawURL(agentId, path)} download={name}>
            <Download size={12} />
            {t("files.download")}
          </a>
        </div>
      ) : md && rendered && (f.content ?? "").trim() ? (
        <div className="fxbody fxmd mt">
          <Markdown text={f.content ?? ""} />
        </div>
      ) : (f.content ?? "") === "" ? (
        <div className="fxmsg small muted">{t("files.emptyFile")}</div>
      ) : (
        <Code text={f.content ?? ""} path={path} />
      )}
    </div>
  );
}

/** A file's text, highlighted, with line numbers. */
function Code({ text, path }: { text: string; path: string }) {
  const lines = useMemo(() => highlightLines(text.replace(/\n$/, ""), langOf(path)), [text, path]);
  return (
    <div className="fxbody fxcode hljs">
      <table>
        <tbody>
          {lines.map((html, i) => (
            <tr key={i}>
              <td className="ln">{i + 1}</td>
              <td className="lc" dangerouslySetInnerHTML={{ __html: html || " " }} />
            </tr>
          ))}
        </tbody>
      </table>
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

// ---------------------------------------------------------------- changes

function Changes({ agent, data, onChanged, onOpen }: { agent: AgentView; data: ChangesView | null; onChanged: (c: ChangesView) => void; onOpen: (p: string) => void }) {
  const [layout, setLayout] = useState<"unified" | "split">(() => (read(LAYOUT_KEY, "unified") === "split" ? "split" : "unified"));
  const [busy, setBusy] = useState(false);
  const [focus, setFocus] = useState<string | null>(null);
  useEffect(() => write(LAYOUT_KEY, layout), [layout]);
  if (!data) return <div className="fxmsg small muted">{t("common.loading")}</div>;
  if (data.files.length === 0)
    return (
      <div className="fxnone">
        <span className="fxnoneic">
          <GitBranch size={20} />
        </span>
        <b>{t("fx.noChanges")}</b>
        <span className="small muted">{data.source === "git" ? t("fx.noChangesGit") : t("fx.noChangesTracked")}</span>
      </div>
    );
  const revert = async (f: FileChange) => {
    const what = f.status === "added" ? t("fx.revertAdded", { name: f.path }) : t("fx.revertOne", { name: f.path });
    if (!(await confirmDialog({ text: what, ok: t("fx.revert"), danger: true }))) return;
    setBusy(true);
    try {
      onChanged(await API.revertChange(agent.id, f.path));
      toast(t("fx.reverted", { name: f.path }));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  const acceptAll = async () => {
    setBusy(true);
    try {
      onChanged(await API.acceptChanges(agent.id));
      toast(t("fx.accepted"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="fxchanges">
      <div className="fxsum">
        <span className="fxsumt">
          <b>{t("fx.nFiles", { n: data.files.length })}</b>
          <span className="add">+{data.added}</span>
          <span className="del">−{data.removed}</span>
          <Bar added={data.added} removed={data.removed} />
        </span>
        <span className="sp" />
        <span className="seg fxmini" role="radiogroup" aria-label={t("fx.layout")}>
          <button type="button" aria-pressed={layout === "unified"} title={t("fx.unified")} aria-label={t("fx.unified")} onClick={() => setLayout("unified")}>
            <Rows3 size={13} />
          </button>
          <button type="button" aria-pressed={layout === "split"} title={t("fx.split")} aria-label={t("fx.split")} onClick={() => setLayout("split")}>
            <Columns2 size={13} />
          </button>
        </span>
        {data.source === "tracked" && (
          <button className="btn sm" type="button" disabled={busy} onClick={() => void acceptAll()} title={t("fx.acceptHint")}>
            {t("fx.acceptAll")}
          </button>
        )}
      </div>
      <p className="fxsrc small muted">{data.source === "git" ? t("fx.srcGit", { branch: data.branch ?? "HEAD" }) : t("fx.srcTracked")}</p>
      <div className="fxlist">
        {data.files.map((f) => (
          <button key={f.path} type="button" className={"fxitem" + (focus === f.path ? " on" : "")} onClick={() => (setFocus(f.path), document.getElementById("fxd-" + f.path)?.scrollIntoView({ block: "start", behavior: "smooth" }))}>
            <span className={"fxst st-" + f.status}>{STATUS_MARK[f.status]}</span>
            <span className="fxip" title={f.from ? `${f.from} → ${f.path}` : f.path}>
              {f.path.includes("/") && <span className="muted">{f.path.slice(0, f.path.lastIndexOf("/") + 1)}</span>}
              {f.path.split("/").pop()}
            </span>
            {!f.binary && !f.large && (
              <span className="fxic">
                <span className="add">+{f.added}</span>
                <span className="del">−{f.removed}</span>
              </span>
            )}
          </button>
        ))}
        {data.more && <p className="small muted fxload">{t("fx.more")}</p>}
      </div>
      <div className="fxdiffs">
        {data.files.map((f) => (
          <DiffCard key={f.path + f.added + f.removed + (f.updated ?? "")} agentId={agent.id} f={f} layout={layout} busy={busy} onRevert={() => void revert(f)} onOpen={() => onOpen(f.path)} />
        ))}
      </div>
    </div>
  );
}

/** GitHub's five-block bar of added and removed lines. */
function Bar({ added, removed }: { added: number; removed: number }) {
  const total = added + removed;
  // as GitHub does: small changes fill fewer blocks, the rest stay grey
  const filled = total === 0 ? 0 : Math.min(5, total < 5 ? total : 5);
  const g = total ? Math.round((added / total) * filled) : 0;
  return (
    <span className="fxbar5" aria-hidden>
      {[0, 1, 2, 3, 4].map((i) => (
        <i key={i} className={i >= filled ? "" : i < g ? "a" : "d"} />
      ))}
    </span>
  );
}

function DiffCard({ agentId, f, layout, busy, onRevert, onOpen }: { agentId: string; f: FileChange; layout: "unified" | "split"; busy: boolean; onRevert: () => void; onOpen: () => void }) {
  const [open, setOpen] = useState(f.added + f.removed < 800);
  const [d, setD] = useState<FileDiff | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    if (!open || d || f.binary || f.large) return;
    API.fileDiff(agentId, f.path).then(setD, (e) => setErr(e?.message ?? String(e)));
  }, [open]);
  const name = f.path.split("/").pop() ?? f.path;
  return (
    <section className="fxdc" id={"fxd-" + f.path}>
      <header className="fxdh">
        <button className="ix" type="button" aria-expanded={open} aria-label={open ? t("fx.collapse") : t("fx.expand")} onClick={() => setOpen(!open)}>
          {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        </button>
        <FileIcon name={name} />
        <button className="fxdname" type="button" onClick={onOpen} title={t("fx.openFile")}>
          {f.from && (
            <>
              <span className="muted">{f.from}</span>
              <span className="muted"> → </span>
            </>
          )}
          {f.path}
        </button>
        <span className={"fxtag st-" + f.status}>{t(`fx.st.${f.status}` as any)}</span>
        {!f.binary && !f.large && (
          <span className="fxic">
            <span className="add">+{f.added}</span>
            <span className="del">−{f.removed}</span>
            <Bar added={f.added} removed={f.removed} />
          </span>
        )}
        <span className="sp" />
        <button className="btn sm ghost fxrevert" type="button" disabled={busy} onClick={onRevert} title={t("fx.revertTitle")}>
          <Undo2 size={12} />
          {t("fx.revert")}
        </button>
      </header>
      {open &&
        (f.binary ? (
          <div className="fxmsg small muted">{t("fx.binary")}</div>
        ) : f.large ? (
          <div className="fxmsg small muted">{t("fx.large")}</div>
        ) : err ? (
          <div className="fxmsg small errt">{err}</div>
        ) : !d ? (
          <div className="fxmsg small muted">{t("common.loading")}</div>
        ) : d.hunks.length === 0 ? (
          <div className="fxmsg small muted">{f.status === "deleted" ? t("fx.emptyDeleted") : t("fx.emptyFile")}</div>
        ) : layout === "split" ? (
          <SplitDiff hunks={d.hunks} path={f.path} />
        ) : (
          <UnifiedDiff hunks={d.hunks} path={f.path} />
        ))}
    </section>
  );
}

/** Highlight each side of the hunks as one text so tokens spanning lines
 *  (comments, strings) read right. */
function useHighlighted(hunks: Hunk[], path: string) {
  return useMemo(() => {
    const lang = langOf(path);
    const oldText: string[] = [];
    const newText: string[] = [];
    for (const h of hunks)
      for (const l of h.lines) {
        if (l.t !== "+") oldText.push(l.s);
        if (l.t !== "-") newText.push(l.s);
      }
    return { o: highlightLines(oldText.join("\n"), lang), n: highlightLines(newText.join("\n"), lang) };
  }, [hunks, path]);
}

function HunkHead({ h, cols }: { h: Hunk; cols: number }) {
  return (
    <tr className="hh">
      <td colSpan={cols}>
        @@ −{h.oldStart},{h.oldLines} +{h.newStart},{h.newLines} @@
      </td>
    </tr>
  );
}

function UnifiedDiff({ hunks, path }: { hunks: Hunk[]; path: string }) {
  const hl = useHighlighted(hunks, path);
  let oi = 0;
  let ni = 0;
  return (
    <div className="fxdiff hljs">
      <table>
        <tbody>
          {hunks.map((h, hi) => (
            <Fragment key={hi}>
              <HunkHead h={h} cols={4} />
              {h.lines.map((l, li) => {
                const html = l.t === "+" ? hl.n[ni++] : l.t === "-" ? hl.o[oi++] : (ni++, hl.o[oi++]);
                return (
                  <tr key={li} className={l.t === "+" ? "ad" : l.t === "-" ? "rm" : ""}>
                    <td className="ln">{l.o ?? ""}</td>
                    <td className="ln">{l.n ?? ""}</td>
                    <td className="mk">{l.t === " " ? "" : l.t === "-" ? "−" : "+"}</td>
                    <td className="lc" dangerouslySetInnerHTML={{ __html: html || " " }} />
                  </tr>
                );
              })}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SplitDiff({ hunks, path }: { hunks: Hunk[]; path: string }) {
  const hl = useHighlighted(hunks, path);
  // pair removed and added runs side by side, context on both sides
  type Cell = { n?: number; html?: string; t: string } | null;
  const rows: { h?: Hunk; l?: Cell; r?: Cell }[] = [];
  let oi = 0;
  let ni = 0;
  for (const h of hunks) {
    rows.push({ h });
    let i = 0;
    while (i < h.lines.length) {
      const l = h.lines[i];
      if (l.t === " ") {
        rows.push({ l: { n: l.o, html: hl.o[oi++], t: " " }, r: { n: l.n, html: hl.n[ni++], t: " " } });
        i++;
        continue;
      }
      const dels: Cell[] = [];
      const adds: Cell[] = [];
      while (i < h.lines.length && h.lines[i].t === "-") dels.push({ n: h.lines[i++].o, html: hl.o[oi++], t: "-" });
      while (i < h.lines.length && h.lines[i].t === "+") adds.push({ n: h.lines[i++].n, html: hl.n[ni++], t: "+" });
      for (let k = 0; k < Math.max(dels.length, adds.length); k++) rows.push({ l: dels[k] ?? null, r: adds[k] ?? null });
    }
  }
  const cell = (c: Cell | undefined, side: "l" | "r") =>
    c ? (
      <>
        <td className={"ln" + (c.t === "-" ? " rm" : c.t === "+" ? " ad" : "")}>{c.n ?? ""}</td>
        <td className={"lc" + (c.t === "-" ? " rm" : c.t === "+" ? " ad" : "") + (side === "l" ? " sl" : "")} dangerouslySetInnerHTML={{ __html: c.html || " " }} />
      </>
    ) : (
      <>
        <td className="ln nil" />
        <td className={"lc nil" + (side === "l" ? " sl" : "")} />
      </>
    );
  return (
    <div className="fxdiff split hljs">
      <table>
        <colgroup>
          <col className="cn" />
          <col />
          <col className="cn" />
          <col />
        </colgroup>
        <tbody>
          {rows.map((r, i) =>
            r.h ? (
              <HunkHead key={i} h={r.h} cols={4} />
            ) : (
              <tr key={i}>
                {cell(r.l, "l")}
                {cell(r.r, "r")}
              </tr>
            ),
          )}
        </tbody>
      </table>
    </div>
  );
}
