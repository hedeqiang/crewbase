import { Skeleton } from "./Skeleton";
import { Fragment, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import {
  AppWindow, BookCheck, ChevronDown, ChevronRight, ChevronUp, Columns2, Download, FileSearch, GitBranch,
  Pencil, RefreshCw, Rows3, Save, Undo2, WrapText, X,
} from "lucide-react";
import { API, rawURL, useRawURL, type AgentView, type ChangesView, type FileChange, type FileContent, type FileDiff, type FileEntry, type Hunk } from "../api";
import { t } from "../i18n";
import { ago } from "../format";
import { setState, toast, useStore } from "../store";
import { Markdown } from "./Markdown";
import { PdfView } from "./PdfView";
import { shortPath } from "./Conversation";
import { confirmDialog } from "./Confirm";
import { previewPath } from "./Preview";
import { highlightLines, langOf } from "../lib/highlight";
import { FileBadge, kindName } from "../lib/fileicons";
import { changedRanges, markRanges } from "../lib/worddiff";
import { PublishBtn, refreshPublished, usePublished } from "./Publish";
import type { LibFileLink } from "../api";

function sizeFmt(n: number): string {
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1).replace(/\.0$/, "") + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

const HTML_RE = /\.html?$/i;
const IMAGE_RE = /\.(png|jpe?g|gif|webp|svg|ico|bmp)$/i;
const VIDEO_RE = /\.(mp4|webm|mov)$/i;
const DOC_RE = /\.(docx?|xlsx?|pptx?|zip|gz|tar|heic|bmp|tiff?)$/i;
const PDF_RE = /\.pdf$/i;

/** A file's icon by its kind: a coloured badge for the languages. */
function FileIcon({ name, size = 14 }: { name: string; size?: number }) {
  return <FileBadge name={name} size={size + 2} />;
}

type View = "files" | "changes";
const VIEW_KEY = "crewbase.files.view";
const WRAP_KEY = "crewbase.files.wrap";
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
  // the open file is the store's (and so the address's: router.ts)
  const sel = useStore((s) => s.fileOpen);
  const setSel = (p: string | null) => setState({ fileOpen: p });
  const [view, setView] = useState<View>(() => (sel ? "files" : read(VIEW_KEY, "files") === "changes" ? "changes" : "files"));
  const [tick, setTick] = useState(0);
  const [changes, setChanges] = useState<ChangesView | null>(null);
  // the conversation's events: changes are looked at again as the agent works
  const evs = useStore((s) => (s.events[agent.session] ?? []).length);

  // a file opened from elsewhere (an attachment, 打开文件) shows it
  useEffect(() => {
    if (sel) setView("files");
  }, [sel]);
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
  const pad = (d: number) => ({ paddingLeft: 8 + d * 14, "--d": d }) as CSSProperties;
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
                  <ChevronRight size={13} className="fxch" />
                  <span className="g">{e.name}</span>
                  {st && <i className="fxdot" title={t("fx.hasChanges")} />}
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
            <button className="fxn2 file" type="button" aria-current={sel === p} style={{ paddingLeft: 8 + depth * 14, "--d": depth } as CSSProperties} onClick={() => onPick(p)} title={`${p}\n${sizeFmt(e.size)} · ${ago(e.modified)}`}>
              <FileIcon name={e.name} />
              <span className={"g" + (st ? " st-" + st : "")}>{e.name}</span>
              {st ? <span className={"fxst st-" + st} title={t(`fx.st.${st}` as any)}>{STATUS_MARK[st] ?? ""}</span> : <span className="fxsz">{sizeFmt(e.size)}</span>}
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
  const [wrap, setWrap] = useState(() => read(WRAP_KEY, "0") === "1");
  useEffect(() => write(WRAP_KEY, wrap ? "1" : "0"), [wrap]);
  // a changed file: which lines, for the gutter
  const [marks, setMarks] = useState<Map<number, string> | null>(null);
  useEffect(() => {
    setMarks(null);
    if (!change || change.binary || change.large || change.status === "deleted") return;
    API.fileDiff(agentId, path).then((d) => setMarks(lineMarks(d.hunks)), () => {});
  }, [agentId, path, change?.added, change?.removed]);
  useEffect(() => {
    API.file(agentId, path)
      .then(setF)
      .catch((e) => setErr(e?.status === 404 ? t("files.missing") : e?.message ?? String(e)));
  }, [agentId, path]);

  const image = IMAGE_RE.test(path) && !/\.svg$/i.test(path);
  const video = VIDEO_RE.test(path);
  const pdf = PDF_RE.test(path);
  const doc = DOC_RE.test(path) || pdf;
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
  const dirs = path.includes("/") ? path.slice(0, path.lastIndexOf("/")).split("/") : [];
  const lines = text ? (f?.content ?? "").replace(/\n$/, "").split("\n").length : 0;
  const kind = kindName(name);

  return (
    <div className="fxview">
      <div className="fxhead">
        <FileBadge name={name} size={20} />
        <span className="fxtitle" title={path}>
          <span className="fxcrumb">
            <Crumbs dirs={dirs} />
            <b>{name}</b>
          </span>
          {f && <span className="fxmeta">{[kind, text ? t("fx.lines", { n: lines }) : "", sizeFmt(f.size)].filter(Boolean).join(" · ")}</span>}
        </span>
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
        {edit === null && text && !(md && rendered) && (
          <button className={"ix" + (wrap ? " on" : "")} type="button" aria-pressed={wrap} aria-label={t("fx.wrap")} title={t("fx.wrap")} onClick={() => setWrap(!wrap)}>
            <WrapText size={13} />
          </button>
        )}
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
        <div className="fxmsg"><Skeleton kind="code" /></div>
      ) : image ? (
        <div className="fxbody fximg">
          <RawImg agent={agentId} path={path} />
        </div>
      ) : video ? (
        <div className="fxbody fximg">
          <RawVideo agent={agentId} path={path} />
        </div>
      ) : pdf ? (
        <PdfView agent={agentId} path={path} />
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
        <Code text={f.content ?? ""} path={path} wrap={wrap} marks={marks} />
      )}
    </div>
  );
}

/** A file's text, highlighted, with line numbers. */
export function Code({ text, path, wrap = true, marks }: { text: string; path: string; wrap?: boolean; marks?: Map<number, string> | null }) {
  const lines = useMemo(() => highlightLines(text.replace(/\n$/, ""), langOf(path)), [text, path]);
  return (
    <div className={"fxbody fxcode hljs" + (wrap ? " wrap" : "")}>
      <table>
        <tbody>
          {lines.map((html, i) => {
            const m = marks?.get(i + 1);
            return (
              <tr key={i} className={m ? "cm-" + m : undefined}>
                <td className="ln" title={m ? t(`fx.mark.${m}` as any) : undefined}>{i + 1}</td>
                <td className="lc" dangerouslySetInnerHTML={{ __html: html || " " }} />
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** Which lines of the new file a diff added or changed, and where lines
 *  were removed (marked on the line after them): the code view's gutter. */
function lineMarks(hunks: Hunk[]): Map<number, string> {
  const m = new Map<number, string>();
  for (const h of hunks) {
    const ls = h.lines;
    let i = 0;
    while (i < ls.length) {
      if (ls[i].t === " ") {
        i++;
        continue;
      }
      let dels = 0;
      while (i < ls.length && ls[i].t === "-") (dels++, i++);
      let k = 0;
      while (i < ls.length && ls[i].t === "+") {
        if (ls[i].n) m.set(ls[i].n!, k < dels ? "mod" : "add");
        k++;
        i++;
      }
      if (dels > k) {
        const next = ls[i]?.n ?? (h.newStart + h.newLines);
        if (next && !m.has(next)) m.set(next, "del");
      }
    }
  }
  return m;
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
  const [sel, setSel] = useState<string | null>(null);
  const pub = usePublished(agent.id);
  useEffect(() => write(LAYOUT_KEY, layout), [layout]);
  // grouped by folder, folders in order, files by name within them
  // a tree of the changed files (a folder with one folder in it shows as "a/b")
  const tree = useMemo(() => changeTree(data?.files ?? []), [data]);
  const [shut, setShut] = useState<Set<string>>(new Set());
  // the files in the order shown, skipping folded folders
  const order = useMemo(() => {
    const out: FileChange[] = [];
    const walk = (ns: CNode[]) => ns.forEach((n) => (n.f ? out.push(n.f) : !shut.has(n.path) && walk(n.kids)));
    walk(tree);
    return out;
  }, [tree, shut]);
  // the file shown: the one picked while it is still changed, else the first
  const cur = order.find((f) => f.path === sel) ?? order[0];
  const at = cur ? order.indexOf(cur) : -1;
  const step = (d: number) => order.length && setSel(order[(at + d + order.length) % order.length].path);
  // J / K between the files (↓ / ↑ in the list), unless typing somewhere
  const stepRef = useRef(step);
  stepRef.current = step;
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (e.metaKey || e.ctrlKey || e.altKey || !el || el.closest("input, textarea, select, [contenteditable=true], .menu, .modal")) return;
      // arrows only in the list (elsewhere they scroll)
      const inList = !!el.closest(".fxclist");
      if (e.key === "j" || (inList && e.key === "ArrowDown")) (e.preventDefault(), stepRef.current(1));
      else if (e.key === "k" || (inList && e.key === "ArrowUp")) (e.preventDefault(), stepRef.current(-1));
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, []);
  if (!data) return <div className="fxmsg"><Skeleton kind="code" /></div>;
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
    <div className="fxchg">
      <div className="fxclist">
        <div className="fxchead">
          <div className="fxcsum">
            <b>{t("fx.nFiles", { n: data.files.length })}</b>
            <span className="add">+{data.added}</span>
            <span className="del">−{data.removed}</span>
            <Bar added={data.added} removed={data.removed} />
          </div>
          <span className="fxcsrc" title={data.source === "git" ? t("fx.srcGit", { branch: data.branch ?? "HEAD" }) : t("fx.srcTracked")}>
            {data.source === "git" ? t("fx.vsBranch", { branch: data.branch ?? "HEAD" }) : t("fx.vsStart")}
          </span>
        </div>
        <div className="fxcfiles" role="listbox" aria-label={t("fx.changes")}>
          {tree.map((n) => (
            <CRow key={n.path} n={n} depth={0} cur={cur?.path} shut={shut} toggle={(p) => setShut((x) => { const y = new Set(x); y.has(p) ? y.delete(p) : y.add(p); return y; })} pick={setSel} pub={pub} />
          ))}
          {data.more && <p className="small muted fxload">{t("fx.more")}</p>}
        </div>
        {data.source === "tracked" && (
          <div className="fxcfoot">
            <button className="btn sm" type="button" disabled={busy} onClick={() => void acceptAll()} title={t("fx.acceptHint")}>
              {t("fx.acceptAll")}
            </button>
          </div>
        )}
      </div>
      {cur && (
        <DiffPane
          key={cur.path + cur.added + cur.removed + (cur.updated ?? "")}
          agentId={agent.id}
          f={cur}
          n={order.length}
          at={at}
          step={step}
          layout={layout}
          setLayout={setLayout}
          busy={busy}
          onRevert={() => void revert(cur)}
          onOpen={() => onOpen(cur.path)}
        />
      )}
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

/** The folders above a file: the nearest two in full, the rest as "…". */
function Crumbs({ dirs }: { dirs: string[] }) {
  const shown = dirs.length > 2 ? ["…", ...dirs.slice(-2)] : dirs;
  return (
    <>
      {shown.map((d, i) => (
        <Fragment key={i}>
          <span className={d === "…" ? "fxell" : undefined}>{d}</span>
          <ChevronRight size={11} />
        </Fragment>
      ))}
    </>
  );
}

/** A node of the changed-files tree: a folder (kids) or a file (f). */
type CNode = { name: string; path: string; kids: CNode[]; f?: FileChange };

function changeTree(files: FileChange[]): CNode[] {
  const root: CNode = { name: "", path: "", kids: [] };
  for (const f of files) {
    const parts = f.path.split("/");
    let at = root;
    parts.forEach((part, i) => {
      const path = parts.slice(0, i + 1).join("/");
      if (i === parts.length - 1) return void at.kids.push({ name: part, path, kids: [], f });
      let d = at.kids.find((k) => !k.f && k.name === part);
      if (!d) at.kids.push((d = { name: part, path, kids: [] }));
      at = d;
    });
  }
  // a folder holding just one folder reads as one row: apps/web
  const squash = (n: CNode): CNode => {
    let x = n;
    while (!x.f && x.kids.length === 1 && !x.kids[0].f) x = { ...x.kids[0], name: x.name + "/" + x.kids[0].name };
    return { ...x, kids: x.kids.map(squash).sort((a, b) => a.name.localeCompare(b.name)) };
  };
  return root.kids.map(squash).sort((a, b) => a.name.localeCompare(b.name));
}

function CRow({ n, depth, cur, shut, toggle, pick, pub }: { n: CNode; depth: number; cur?: string; shut: Set<string>; toggle: (p: string) => void; pick: (p: string) => void; pub: Map<string, LibFileLink> }) {
  const pad = { paddingLeft: 10 + depth * 14, "--d": depth } as CSSProperties;
  if (n.f) {
    const f = n.f;
    return (
      <button type="button" role="option" aria-selected={cur === f.path} className="fxcrow" style={pad} onClick={() => pick(f.path)} title={f.from ? `${f.from} → ${f.path}` : f.path}>
        <FileBadge name={n.name} />
        <span className={"g st-" + f.status}>{n.name}</span>
        {pub.has(f.path) && <PubMark l={pub.get(f.path)!} />}
        {!f.binary && !f.large && (
          <span className="fxcn">
            {f.added > 0 && <span className="add">+{f.added}</span>}
            {f.removed > 0 && <span className="del">−{f.removed}</span>}
          </span>
        )}
        <span className={"fxst st-" + f.status} title={t(`fx.st.${f.status}` as any)}>
          {STATUS_MARK[f.status]}
        </span>
      </button>
    );
  }
  const open = !shut.has(n.path);
  return (
    <>
      <button type="button" className="fxcrow dir" style={pad} aria-expanded={open} onClick={() => toggle(n.path)} title={n.path}>
        <ChevronRight size={13} className="fxch" />
        <span className="g">{n.name}</span>
        <i className="fxdot" />
      </button>
      {open && n.kids.map((k) => <CRow key={k.path} n={k} depth={depth + 1} cur={cur} shut={shut} toggle={toggle} pick={pick} pub={pub} />)}
    </>
  );
}

/** A published file in the 改动 list: in the library, and whether a side moved on. */
function PubMark({ l }: { l: LibFileLink }) {
  const label = l.state === "file" ? t("pub.stFile") : l.state === "doc" ? t("pub.stDoc") : l.state === "both" ? t("pub.stBoth") : t("pub.stSame");
  return (
    <span className={"fxpub pub-" + (l.state || "same")} title={label} aria-label={label}>
      <BookCheck size={11} />
    </span>
  );
}

/** The diff of the file picked in 改动. */
function DiffPane({ agentId, f, n, at, step, layout, setLayout, busy, onRevert, onOpen }: {
  agentId: string; f: FileChange; n: number; at: number; step: (d: number) => void;
  layout: "unified" | "split"; setLayout: (l: "unified" | "split") => void; busy: boolean; onRevert: () => void; onOpen: () => void;
}) {
  const [d, setD] = useState<FileDiff | null>(null);
  const [err, setErr] = useState("");
  const [rev, setRev] = useState(0);
  useEffect(() => {
    if (f.binary || f.large) return;
    API.fileDiff(agentId, f.path).then(setD, (e) => setErr(e?.message ?? String(e)));
  }, [rev]);
  // the file changed: how it stands against its document may have too
  useEffect(() => refreshPublished(agentId), []);
  const session = useStore((s) => s.current) ?? undefined;
  const name = f.path.split("/").pop() ?? f.path;
  const dirs = f.path.includes("/") ? f.path.slice(0, f.path.lastIndexOf("/")).split("/") : [];
  // a narrow panel has no room for two columns: one, whatever was picked
  const box = useRef<HTMLElement>(null);
  const [narrow, setNarrow] = useState(false);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setNarrow(el.clientWidth < 520));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return (
    <section className="fxview fxcview" ref={box}>
      <header className="fxhead">
        <FileBadge name={name} size={20} />
        <span className="fxtitle" title={f.from ? `${f.from} → ${f.path}` : f.path}>
          <span className="fxcrumb">
            <Crumbs dirs={dirs} />
            <b>{name}</b>
          </span>
          <span className="fxmeta">
            <span className={"st-" + f.status}>{t(`fx.st.${f.status}` as any)}</span>
            {f.from && <> · {t("fx.from", { path: f.from })}</>}
            {!f.binary && !f.large && (
              <>
                {" · "}
                <span className="add">+{f.added}</span> <span className="del">−{f.removed}</span>
              </>
            )}
          </span>
        </span>
        <span className="sp" />
        {n > 1 && (
          <span className="fxnav">
            <button className="ix" type="button" aria-label={t("fx.prevFile")} title={t("fx.prevFile") + " (K)"} onClick={() => step(-1)}>
              <ChevronUp size={14} />
            </button>
            <span className="fxnavn">
              {at + 1}/{n}
            </span>
            <button className="ix" type="button" aria-label={t("fx.nextFile")} title={t("fx.nextFile") + " (J)"} onClick={() => step(1)}>
              <ChevronDown size={14} />
            </button>
          </span>
        )}
        <span className="seg fxmini" role="radiogroup" aria-label={t("fx.layout")}>
          <button type="button" aria-pressed={layout === "unified"} title={t("fx.unified")} aria-label={t("fx.unified")} onClick={() => setLayout("unified")}>
            <Rows3 size={13} />
          </button>
          <button type="button" aria-pressed={layout === "split"} title={t("fx.split")} aria-label={t("fx.split")} onClick={() => setLayout("split")}>
            <Columns2 size={13} />
          </button>
        </span>
        {f.status !== "deleted" && (
          <button className="ix" type="button" aria-label={t("fx.openFile")} title={t("fx.openFile")} onClick={onOpen}>
            <FileSearch size={13} />
          </button>
        )}
        {f.status !== "deleted" && <PublishBtn agent={agentId} path={f.path} from={{ session }} compact={narrow} onPulled={() => setRev((r) => r + 1)} />}
        <button className="btn sm ghost fxrevert" type="button" disabled={busy} onClick={onRevert} title={t("fx.revertTitle")}>
          <Undo2 size={12} />
          {t("fx.revert")}
        </button>
      </header>
      <div className="fxbody">
        {f.binary ? (
          <div className="fxmsg small muted">{t("fx.binary")}</div>
        ) : f.large ? (
          <div className="fxmsg small muted">{t("fx.large")}</div>
        ) : err ? (
          <div className="fxmsg small errt">{err}</div>
        ) : !d ? (
          <div className="fxmsg"><Skeleton kind="code" /></div>
        ) : d.hunks.length === 0 ? (
          <div className="fxmsg small muted">{f.status === "deleted" ? t("fx.emptyDeleted") : t("fx.emptyFile")}</div>
        ) : layout === "split" && !narrow ? (
          <SplitDiff hunks={d.hunks} path={f.path} />
        ) : (
          <UnifiedDiff hunks={d.hunks} path={f.path} />
        )}
      </div>
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
    const o = highlightLines(oldText.join("\n"), lang);
    const n = highlightLines(newText.join("\n"), lang);
    // an edited line: mark the words that changed (a removed line paired
    // with the added line in the same place of its run)
    let oi = 0;
    let ni = 0;
    for (const h of hunks) {
      const ls = h.lines;
      let i = 0;
      while (i < ls.length) {
        if (ls[i].t === " ") {
          i++, oi++, ni++;
          continue;
        }
        const dels: { s: string; at: number }[] = [];
        const adds: { s: string; at: number }[] = [];
        while (i < ls.length && ls[i].t === "-") dels.push({ s: ls[i++].s, at: oi++ });
        while (i < ls.length && ls[i].t === "+") adds.push({ s: ls[i++].s, at: ni++ });
        if (dels.length && adds.length && dels.length <= 30 && adds.length <= 30)
          for (let k = 0; k < Math.min(dels.length, adds.length); k++) {
            const c = changedRanges(dels[k].s, adds[k].s);
            if (!c) continue;
            o[dels[k].at] = markRanges(o[dels[k].at] ?? "", c.a);
            n[adds[k].at] = markRanges(n[adds[k].at] ?? "", c.b);
          }
      }
    }
    return { o, n };
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

export function UnifiedDiff({ hunks, path }: { hunks: Hunk[]; path: string }) {
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
