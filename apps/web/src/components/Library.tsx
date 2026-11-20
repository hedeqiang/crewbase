// 文档库: the team's documents — what people uploaded, what agents saved and
// the images and videos they generated (features/library). A full page for
// browsing and managing, and a compact right-panel tab to drag documents
// into the conversation.

import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent } from "react";
import {
  Bot,
  Download,
  ExternalLink,
  FileImage,
  FileSpreadsheet,
  FileText,
  File as FileIcon,
  Globe,
  History,
  Inbox,
  LayoutGrid,
  Library as LibraryIcon,
  List,
  Lock,
  Paperclip,
  RotateCcw,
  Search,
  Sparkles,
  Trash2,
  Upload,
  User,
  X,
  Film,
  Presentation,
  Tag,
} from "lucide-react";
import { API, libRawText, libRawURL, uploadLibrary, useLibRawURL, type LibFolder, type LibItem, type LibSource, type LibStats, type LibVersion } from "../api";
import { t } from "../i18n";
import { ago } from "../format";
import { openLibrary, openSession, sessionAgent, setState, toast, useStore } from "../store";
import { addLibraryFile, fmtSize, LIB_DRAG, type LibDrag } from "./Attachments";
import { confirmDialog } from "./Confirm";
import { ListToggle } from "./Header";
import { Head } from "./Settings";
import { Markdown } from "./Markdown";
import { Code } from "./Files";

const FOLDERS: LibFolder[] = ["inbox", "docs", "media", "pages"];
const SOURCES: LibSource[] = ["upload", "person", "agent", "generated"];
const VIEW_KEY = "crewbase.library.view";

const FOLDER_ICON: Record<LibFolder, typeof Inbox> = { inbox: Inbox, docs: FileText, media: FileImage, pages: Globe };
const SOURCE_ICON: Record<LibSource, typeof Inbox> = { upload: Paperclip, person: User, agent: Bot, generated: Sparkles };

const isImage = (it: LibItem) => /^image\/(png|jpeg|gif|webp)$/.test(it.mime);
const isVideo = (it: LibItem) => /^video\/(mp4|webm)$/.test(it.mime);
const isMarkdown = (it: LibItem) => /\.(md|markdown)$/i.test(it.name);
const isHTML = (it: LibItem) => /\.html?$/i.test(it.name);
const isPlain = (it: LibItem) =>
  it.size < 1 << 20 && (it.mime.startsWith("text/") || /\.(json|ya?ml|toml|csv|tsv|txt|log|xml|sql|sh|py|js|ts|tsx|go|rs|java|rb|css)$/i.test(it.name));

function ItemIcon({ it, size = 16 }: { it: LibItem; size?: number }) {
  const ext = it.name.toLowerCase().split(".").pop() ?? "";
  if (it.mime.startsWith("image/")) return <FileImage size={size} />;
  if (it.mime.startsWith("video/")) return <Film size={size} />;
  if (/^(xlsx?|csv|tsv)$/.test(ext)) return <FileSpreadsheet size={size} />;
  if (/^pptx?$/.test(ext)) return <Presentation size={size} />;
  if (/^html?$/.test(ext)) return <Globe size={size} />;
  if (it.mime === "application/pdf" || /^(docx?|md|txt)$/.test(ext) || it.mime.startsWith("text/")) return <FileText size={size} />;
  return <FileIcon size={size} />;
}

/** «match» → <mark>. */
function Snippet({ text }: { text: string }) {
  const parts = text.replace(/\s+/g, " ").split(/(«[^»]*»)/);
  return (
    <span className="libsnip">
      {parts.map((p, i) => (p.startsWith("«") && p.endsWith("»") ? <mark key={i}>{p.slice(1, -1)}</mark> : <span key={i}>{p}</span>))}
    </span>
  );
}

function useAgentName() {
  const agents = useStore((s) => s.agents);
  return (id?: string) => (id ? agents[id]?.name || id : "");
}

function sourceLine(it: LibItem, agentName: (id?: string) => string): string {
  switch (it.source) {
    case "upload":
      return it.agent ? t("lib.src.uploadTo", { agent: agentName(it.agent) }) : t("lib.src.upload");
    case "person":
      return t("lib.src.person");
    case "generated":
      return t("lib.src.generatedBy", { agent: agentName(it.agent) });
    default:
      return t("lib.src.savedBy", { agent: agentName(it.agent) });
  }
}

interface Filter {
  q: string;
  folder: string;
  source: string;
  tag: string;
}

/** The team's documents for a filter, refreshed every 15 s while shown. */
function useLibrary(team: string, f: Filter) {
  const [items, setItems] = useState<LibItem[] | null>(null);
  const [stats, setStats] = useState<LibStats | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((n) => n + 1), []);
  useEffect(() => {
    let live = true;
    const h = setTimeout(
      () => {
        API.library(team, { q: f.q.trim(), folder: f.folder, source: f.source, tag: f.tag, limit: 300 })
          .then((r) => {
            if (!live) return;
            setItems(r.items);
            if (r.stats) setStats(r.stats);
            setErr(null);
          })
          .catch((e) => live && setErr(e?.message ?? String(e)));
      },
      f.q ? 180 : 0,
    );
    return () => {
      live = false;
      clearTimeout(h);
    };
  }, [team, f.q, f.folder, f.source, f.tag, tick]);
  useEffect(() => {
    const h = setInterval(() => document.visibilityState === "visible" && reload(), 15_000);
    return () => clearInterval(h);
  }, [reload]);
  return { items, stats, err, reload };
}

function dragData(it: LibItem): LibDrag {
  return { team: it.team, id: it.id, name: it.name, mime: it.mime, size: it.size };
}

function onDragStart(e: DragEvent, it: LibItem) {
  e.dataTransfer.setData(LIB_DRAG, JSON.stringify(dragData(it)));
  e.dataTransfer.setData("text/plain", it.title || it.name);
  e.dataTransfer.effectAllowed = "copy";
}

/** The conversation a document can be attached to (the one open, with an agent that has a workspace). */
function useTarget() {
  const current = useStore((s) => s.current);
  const agents = useStore((s) => s.agents);
  const sessions = useStore((s) => s.sessions);
  const channels = useStore((s) => s.channels);
  return useMemo(() => {
    if (!current || channels[current]) return null;
    const a = sessionAgent(current);
    if (!a || a.kind === "chat") return null;
    return { session: current, agent: a };
  }, [current, agents, sessions, channels]);
}

function attachTo(target: { session: string; agent: { id: string } }, it: LibItem) {
  addLibraryFile(target.session, target.agent.id, dragData(it));
}

/** Files dropped on the page are added to the library. */
async function addFilesTo(team: string, files: File[], folder?: string): Promise<LibItem[]> {
  const out: LibItem[] = [];
  for (const f of files) {
    try {
      out.push(await uploadLibrary(team, f, f.name, folder));
    } catch (e: any) {
      toast(t("lib.addFailed", { name: f.name, error: e?.message ?? String(e) }));
    }
  }
  if (out.length) toast(t("lib.added", { n: out.length }));
  return out;
}

// ---------------------------------------------------------------- page

/** The 文档库 page (设置 › 文档库; embedded: in the settings column, filters as a row). */
export function LibraryPage({ embedded = false }: { embedded?: boolean }) {
  const team = useStore((s) => s.team);
  const teams = useStore((s) => s.teams);
  const listOpen = useStore((s) => s.listOpen);
  const sel = useStore((s) => s.libOpen);
  const [f, setF] = useState<Filter>({ q: "", folder: "", source: "", tag: "" });
  const [grid, setGrid] = useState(() => {
    try {
      return localStorage.getItem(VIEW_KEY) === "grid";
    } catch {
      return false;
    }
  });
  const [dropping, setDropping] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const { items, stats, err, reload } = useLibrary(team, f);
  const agentName = useAgentName();
  const teamName = teams.length > 1 ? teams.find((x) => x.id === team)?.name : "";
  const showGrid = grid || f.folder === "media";

  const setView = (g: boolean) => {
    setGrid(g);
    try {
      localStorage.setItem(VIEW_KEY, g ? "grid" : "list");
    } catch {
      /* private mode */
    }
  };
  const add = async (files: File[]) => {
    if (!files.length) return;
    const added = await addFilesTo(team, files, f.folder && f.folder !== "media" ? f.folder : undefined);
    reload();
    if (added.length === 1) setState({ libOpen: added[0].id });
  };
  const filtered = !!(f.q || f.folder || f.source || f.tag);

  return (
    <div
      className={(embedded ? "lib embedded" : "center lib") + (dropping ? " dropping" : "")}
      onDragOver={(e) => {
        if (!Array.from(e.dataTransfer.types).includes("Files")) return;
        e.preventDefault();
        setDropping(true);
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropping(false);
      }}
      onDrop={(e) => {
        if (!Array.from(e.dataTransfer.types).includes("Files")) return;
        e.preventDefault();
        setDropping(false);
        void add(Array.from(e.dataTransfer.files));
      }}
    >
      {embedded ? (
        <Head
          title={t("lib.title")}
          sub={teamName ? t("lib.subTeam", { team: teamName }) : t("lib.sub")}
          right={
            <>
              <button className="btn sm" type="button" onClick={() => fileInput.current?.click()}>
            <Upload size={12} />
            {t("lib.add")}
          </button>
          <input
            ref={fileInput}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              void add(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
            </>
          }
        />
      ) : (
      <div className="chead libhead">
        {!listOpen && <ListToggle />}
        <span className="who">
          <LibraryIcon size={16} />
          <span className="nm">{t("lib.title")}</span>
          {teamName && <span className="role">{teamName}</span>}
        </span>
        <span className="tr">
          <button className="btn sm" type="button" onClick={() => fileInput.current?.click()}>
            <Upload size={12} />
            {t("lib.add")}
          </button>
          <input
            ref={fileInput}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              void add(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
        </span>
      </div>
      )}
      <div className={"libbody" + (sel ? " withdetail" : "")}>
        <nav className="libnav" aria-label={t("lib.filters")}>
          <button type="button" className="lnv" aria-current={!f.folder && !f.source && !f.tag} onClick={() => setF({ ...f, folder: "", source: "", tag: "" })}>
            <LibraryIcon size={15} />
            <span>{t("lib.all")}</span>
            <span className="cnt">{stats?.total ?? ""}</span>
          </button>
          <div className="lnsec">{t("lib.byType")}</div>
          {FOLDERS.map((x) => {
            const Ic = FOLDER_ICON[x];
            return (
              <button key={x} type="button" className="lnv" aria-current={f.folder === x} onClick={() => setF({ ...f, folder: f.folder === x ? "" : x })}>
                <Ic size={15} />
                <span>{t(`lib.folder.${x}` as any)}</span>
                <span className="cnt">{stats?.folders[x] ?? 0}</span>
              </button>
            );
          })}
          <div className="lnsec">{t("lib.bySource")}</div>
          {SOURCES.filter((x) => (stats?.sources[x] ?? 0) > 0 || f.source === x).map((x) => {
            const Ic = SOURCE_ICON[x];
            return (
              <button key={x} type="button" className="lnv" aria-current={f.source === x} onClick={() => setF({ ...f, source: f.source === x ? "" : x })}>
                <Ic size={15} />
                <span>{t(`lib.source.${x}` as any)}</span>
                <span className="cnt">{stats?.sources[x] ?? 0}</span>
              </button>
            );
          })}
          {!!stats?.tags.length && (
            <>
              <div className="lnsec">{t("lib.tags")}</div>
              <div className="libtags">
                {stats.tags.slice(0, 30).map((x) => (
                  <button key={x.tag} type="button" className="libtag" aria-pressed={f.tag === x.tag} onClick={() => setF({ ...f, tag: f.tag === x.tag ? "" : x.tag })}>
                    {x.tag}
                    <span>{x.n}</span>
                  </button>
                ))}
              </div>
            </>
          )}
        </nav>

        <section className="libmain">
          <div className="libbar">
            <label className="search">
              <Search size={14} />
              <input value={f.q} onChange={(e) => setF({ ...f, q: e.target.value })} placeholder={t("lib.search")} />
              {f.q && (
                <button className="ix" type="button" aria-label={t("lib.clear")} onClick={() => setF({ ...f, q: "" })}>
                  <X size={12} />
                </button>
              )}
            </label>
            <span className="seg">
              <button type="button" aria-pressed={!showGrid} aria-label={t("lib.viewList")} title={t("lib.viewList")} onClick={() => setView(false)}>
                <List size={14} />
              </button>
              <button type="button" aria-pressed={showGrid} aria-label={t("lib.viewGrid")} title={t("lib.viewGrid")} onClick={() => setView(true)}>
                <LayoutGrid size={14} />
              </button>
            </span>
          </div>
          {f.tag && (
            <div className="libactive">
              <Tag size={12} />
              {f.tag}
              <button className="ix" type="button" aria-label={t("lib.clear")} onClick={() => setF({ ...f, tag: "" })}>
                <X size={11} />
              </button>
            </div>
          )}
          <div className="libscroll">
            {err ? (
              <p className="small libmsg">{err}</p>
            ) : items === null ? (
              <p className="small muted libmsg">{t("common.loading")}</p>
            ) : items.length === 0 ? (
              filtered ? (
                <p className="small muted libmsg">{t("lib.noMatch")}</p>
              ) : (
                <div className="libempty">
                  <LibraryIcon size={28} />
                  <h3>{t("lib.emptyTitle")}</h3>
                  <p>{t("lib.emptyBody")}</p>
                  <button className="btn pri" type="button" onClick={() => fileInput.current?.click()}>
                    <Upload size={13} />
                    {t("lib.add")}
                  </button>
                </div>
              )
            ) : showGrid ? (
              <div className="libgrid">
                {items.map((it) => (
                  <GridCard key={it.id} it={it} sel={sel === it.id} />
                ))}
              </div>
            ) : (
              <div className="librows" role="list">
                {items.map((it) => (
                  <button
                    key={it.id}
                    type="button"
                    role="listitem"
                    className="librow"
                    aria-current={sel === it.id}
                    draggable
                    onDragStart={(e) => onDragStart(e, it)}
                    onClick={() => setState({ libOpen: sel === it.id ? null : it.id })}
                  >
                    <span className={"libic f-" + it.folder}>
                      <ItemIcon it={it} />
                    </span>
                    <span className="g">
                      <b>
                        {it.title || it.name}
                        {it.private && <Lock size={11} className="lock" />}
                      </b>
                      {it.snippet ? (
                        <Snippet text={it.snippet} />
                      ) : (
                        <span>
                          {it.title && it.title !== it.name ? it.name + " · " : ""}
                          {sourceLine(it, agentName)}
                        </span>
                      )}
                    </span>
                    <span className="libtg">
                      {(it.tags ?? []).slice(0, 2).map((x) => (
                        <span key={x} className="pill sm">
                          {x}
                        </span>
                      ))}
                    </span>
                    <span className="libsz">{fmtSize(it.size)}</span>
                    <span className="libwhen" title={new Date(it.updated).toLocaleString()}>
                      {ago(it.updated)}
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>
        </section>

        {sel !== null && <Detail key={sel} team={team} id={sel} onChanged={reload} />}
      </div>
      {dropping && <div className="libdrop">{t("lib.dropHere")}</div>}
    </div>
  );
}

function GridCard({ it, sel }: { it: LibItem; sel: boolean }) {
  const media = isImage(it) || isVideo(it);
  const src = useLibRawURL(it.team, it.id);
  return (
    <button
      type="button"
      className="libcard"
      aria-current={sel}
      draggable
      onDragStart={(e) => onDragStart(e, it)}
      onClick={() => setState({ libOpen: sel ? null : it.id })}
      title={it.note || it.title || it.name}
    >
      <span className="thumb">
        {isImage(it) && src ? (
          <img src={src} alt="" loading="lazy" />
        ) : isVideo(it) && src ? (
          <video src={src} preload="metadata" muted playsInline />
        ) : (
          <span className={"libic big f-" + it.folder}>
            <ItemIcon it={it} size={26} />
          </span>
        )}
        {media && it.source === "generated" && (
          <span className="badge">
            <Sparkles size={10} />
          </span>
        )}
      </span>
      <span className="cap">
        <b>{it.title || it.name}</b>
        <span>{ago(it.updated)}</span>
      </span>
    </button>
  );
}

// ---------------------------------------------------------------- detail

function Detail({ team, id, onChanged }: { team: string; id: number; onChanged: () => void }) {
  const [data, setData] = useState<{ item: LibItem; versions: LibVersion[] } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [tick, setTick] = useState(0);
  const target = useTarget();
  const agentName = useAgentName();
  useEffect(() => {
    API.libraryItem(team, id)
      .then(setData)
      .catch((e) => setErr(e?.status === 404 ? t("lib.gone") : e?.message ?? String(e)));
  }, [team, id, tick]);
  const close = () => setState({ libOpen: null });
  const changed = (it?: LibItem) => {
    if (it) setData((d) => (d ? { ...d, item: it } : d));
    setTick((n) => n + 1);
    onChanged();
  };

  if (err)
    return (
      <aside className="libdetail">
        <div className="libdh">
          <span className="sp" />
          <button className="ix" type="button" aria-label={t("common.close")} onClick={close}>
            <X size={13} />
          </button>
        </div>
        <p className="small libmsg">{err}</p>
      </aside>
    );
  if (!data) return <aside className="libdetail" />;
  const it = data.item;
  const remove = async () => {
    if (!(await confirmDialog({ text: t("lib.deleteConfirm", { name: it.title || it.name }), danger: true, ok: t("common.delete") }))) return;
    try {
      await API.deleteLibrary(team, it.id);
      close();
      onChanged();
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const useIn = () => {
    if (!target) return;
    attachTo(target, it);
    openSession(target.session);
    toast(t("lib.attached", { name: it.name, agent: target.agent.name }));
  };

  return (
    <aside className="libdetail" aria-label={it.title || it.name}>
      <div className="libdh">
        <span className={"libic f-" + it.folder}>
          <ItemIcon it={it} />
        </span>
        <span className="g">
          <b title={it.name}>{it.title || it.name}</b>
          <span>
            {t(`lib.folder.${it.folder}` as any)} · {fmtSize(it.size)}
          </span>
        </span>
        <a className="ix" href={libRawURL(team, it.id)} download={it.name} aria-label={t("files.download")} title={t("files.download")}>
          <Download size={13} />
        </a>
        <button className="ix" type="button" aria-label={t("common.close")} onClick={close}>
          <X size={13} />
        </button>
      </div>
      <div className="libdscroll">
        <div className="libactions">
          <button className="btn pri sm" type="button" disabled={!target} onClick={useIn} title={target ? "" : t("lib.useNoTarget")}>
            <Paperclip size={12} />
            {target ? t("lib.useIn", { agent: target.agent.name }) : t("lib.useInConv")}
          </button>
          <span className="small muted">{t("lib.dragHint")}</span>
        </div>
        <ItemPreview key={it.sha256} team={team} it={it} />
        <dl className="libmeta">
          <dt>{t("lib.meta.source")}</dt>
          <dd>{sourceLine(it, agentName)}</dd>
          {it.session && (
            <>
              <dt>{t("lib.meta.conversation")}</dt>
              <dd>
                <button className="linkbtn" type="button" onClick={() => it.session && openSession(it.session)}>
                  {t("lib.openConversation")}
                </button>
              </dd>
            </>
          )}
          {it.note && (
            <>
              <dt>{it.source === "generated" ? t("lib.meta.prompt") : t("lib.meta.note")}</dt>
              <dd className="note">{it.note}</dd>
            </>
          )}
          <dt>{t("lib.meta.file")}</dt>
          <dd className="mono small">{it.name}</dd>
          <dt>{t("lib.meta.added")}</dt>
          <dd>{new Date(it.created).toLocaleString()}</dd>
          {it.updated !== it.created && (
            <>
              <dt>{t("lib.meta.updated")}</dt>
              <dd>{new Date(it.updated).toLocaleString()}</dd>
            </>
          )}
          <dt>{t("lib.meta.search")}</dt>
          <dd>{it.text ? t("lib.textYes") : t("lib.textNo")}</dd>
        </dl>
        <Edits team={team} it={it} onSaved={changed} />
        {data.versions.length > 0 && <Versions team={team} it={it} versions={data.versions} onRestored={changed} />}
        <div className="libdanger">
          <button className="btn dng sm" type="button" onClick={() => void remove()}>
            <Trash2 size={12} />
            {t("lib.delete")}
          </button>
        </div>
      </div>
    </aside>
  );
}

function ItemPreview({ team, it }: { team: string; it: LibItem }) {
  const src = useLibRawURL(team, it.id);
  const [text, setText] = useState<string | null>(null);
  const [extracted, setExtracted] = useState<string | null>(null);
  const [rendered, setRendered] = useState(true);
  const plain = isPlain(it) || isMarkdown(it) || isHTML(it);
  useEffect(() => {
    if (isImage(it) || isVideo(it)) return;
    if (plain) {
      libRawText(team, it.id)
        .then(setText)
        .catch(() => setText(""));
    } else if (it.text) {
      API.libraryText(team, it.id)
        .then((r) => setExtracted(r.text))
        .catch(() => setExtracted(""));
    }
  }, [team, it.id, it.sha256]);

  if (isImage(it)) return <div className="libpv img">{src && <img src={src} alt={it.note || it.name} />}</div>;
  if (isVideo(it)) return <div className="libpv img">{src && <video src={src} controls preload="metadata" playsInline />}</div>;
  if (plain) {
    if (text === null) return <div className="libpv msg small muted">{t("common.loading")}</div>;
    if (!text.trim()) return <div className="libpv msg small muted">{t("files.emptyFile")}</div>;
    const md = isMarkdown(it);
    const html = isHTML(it);
    return (
      <div className="libpv">
        {(md || html) && (
          <div className="libpvbar">
            <span className="seg fxmini">
              <button type="button" aria-pressed={rendered} onClick={() => setRendered(true)}>
                {t("fx.rendered")}
              </button>
              <button type="button" aria-pressed={!rendered} onClick={() => setRendered(false)}>
                {t("fx.source")}
              </button>
            </span>
          </div>
        )}
        {md && rendered ? (
          <div className="fxmd mt libmd">
            <Markdown text={text} />
          </div>
        ) : html && rendered ? (
          // a static picture of the page: no scripts, its own origin
          <iframe className="libhtml" sandbox="" srcDoc={text} title={it.name} />
        ) : (
          <Code text={text} path={it.name} />
        )}
      </div>
    );
  }
  if (extracted === null && it.text) return <div className="libpv msg small muted">{t("common.loading")}</div>;
  if (extracted)
    return (
      <div className="libpv">
        <div className="libpvbar small muted">{t("lib.extracted")}</div>
        <div className="libtext">{extracted.length > 20000 ? extracted.slice(0, 20000) + "\n…" : extracted}</div>
      </div>
    );
  return (
    <div className="libpv msg">
      <span className="small muted">{t("lib.noPreview")}</span>
      <a className="btn sm" href={libRawURL(team, it.id)} download={it.name}>
        <Download size={12} />
        {t("files.download")}
      </a>
    </div>
  );
}

function Edits({ team, it, onSaved }: { team: string; it: LibItem; onSaved: (it: LibItem) => void }) {
  const [title, setTitle] = useState(it.title ?? "");
  const [tag, setTag] = useState("");
  useEffect(() => setTitle(it.title ?? ""), [it.title]);
  const patch = async (b: { title?: string; tags?: string[]; private?: boolean }) => {
    try {
      onSaved(await API.patchLibrary(team, it.id, b));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  const tags = it.tags ?? [];
  const addTag = () => {
    const v = tag.trim().replace(/,/g, " ");
    setTag("");
    if (v && !tags.includes(v)) void patch({ tags: [...tags, v] });
  };
  return (
    <div className="libedit">
      <label className="lbl">
        {t("lib.meta.title")}
        <input
          className="fi sm"
          value={title}
          placeholder={it.name}
          onChange={(e) => setTitle(e.target.value)}
          onBlur={() => title !== (it.title ?? "") && void patch({ title })}
          onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
        />
      </label>
      <div className="lbl">
        {t("lib.tags")}
        <div className="libtagedit">
          {tags.map((x) => (
            <span key={x} className="libtag on">
              {x}
              <button type="button" aria-label={t("lib.removeTag", { tag: x })} onClick={() => void patch({ tags: tags.filter((y) => y !== x) })}>
                <X size={10} />
              </button>
            </span>
          ))}
          <input
            value={tag}
            placeholder={t("lib.addTag")}
            onChange={(e) => setTag(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === ",") {
                e.preventDefault();
                addTag();
              }
            }}
            onBlur={addTag}
          />
        </div>
      </div>
      {it.agent && (
        <label className="libpriv">
          <input type="checkbox" checked={!!it.private} onChange={(e) => void patch({ private: e.target.checked })} />
          <span>
            <b>{t("lib.private")}</b>
            <span className="small muted">{t("lib.privateDesc")}</span>
          </span>
        </label>
      )}
    </div>
  );
}

function Versions({ team, it, versions, onRestored }: { team: string; it: LibItem; versions: LibVersion[]; onRestored: (it: LibItem) => void }) {
  const agentName = useAgentName();
  const restore = async (v: LibVersion) => {
    if (!(await confirmDialog({ text: t("lib.restoreConfirm", { when: new Date(v.at).toLocaleString() }), ok: t("lib.restore") }))) return;
    try {
      onRestored(await API.restoreLibrary(team, it.id, v.id));
      toast(t("lib.restored"));
    } catch (e: any) {
      toast(e?.message ?? String(e));
    }
  };
  return (
    <div className="libvers">
      <div className="lnsec">
        <History size={12} />
        {t("lib.versions", { n: versions.length })}
      </div>
      {versions.map((v) => (
        <div key={v.id} className="libver">
          <span className="g">
            <b>{new Date(v.at).toLocaleString()}</b>
            <span>
              {v.agent ? agentName(v.agent) : v.source === "person" || v.source === "upload" ? t("lib.src.person") : ""} · {fmtSize(v.size)}
            </span>
          </span>
          <a className="ix" href={libRawURL(team, it.id, v.id)} download={it.name} aria-label={t("files.download")} title={t("files.download")}>
            <Download size={12} />
          </a>
          <button className="ix" type="button" aria-label={t("lib.restore")} title={t("lib.restore")} onClick={() => void restore(v)}>
            <RotateCcw size={12} />
          </button>
        </div>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------- panel tab

/** The right panel's 文档库 tab: find a document and drag it (or click +)
 *  into the conversation. */
export function LibraryTab() {
  const team = useStore((s) => s.team);
  const [f, setF] = useState<Filter>({ q: "", folder: "", source: "", tag: "" });
  const { items, err } = useLibrary(team, f);
  const target = useTarget();
  const agentName = useAgentName();
  return (
    <div className="libtab">
      <div className="libtabbar">
        <label className="search">
          <Search size={13} />
          <input value={f.q} onChange={(e) => setF({ ...f, q: e.target.value })} placeholder={t("lib.search")} />
        </label>
        <button className="ix" type="button" title={t("lib.openPage")} aria-label={t("lib.openPage")} onClick={() => openLibrary()}>
          <ExternalLink size={14} />
        </button>
      </div>
      <div className="libtabf">
        <button type="button" className="libtag" aria-pressed={!f.folder} onClick={() => setF({ ...f, folder: "" })}>
          {t("lib.all")}
        </button>
        {FOLDERS.map((x) => (
          <button key={x} type="button" className="libtag" aria-pressed={f.folder === x} onClick={() => setF({ ...f, folder: f.folder === x ? "" : x })}>
            {t(`lib.folder.${x}` as any)}
          </button>
        ))}
      </div>
      <div className="libtabl">
        {err ? (
          <p className="small libmsg">{err}</p>
        ) : items === null ? (
          <p className="small muted libmsg">{t("common.loading")}</p>
        ) : items.length === 0 ? (
          <p className="small muted libmsg">{f.q || f.folder ? t("lib.noMatch") : t("lib.tabEmpty")}</p>
        ) : (
          items.slice(0, 100).map((it) => (
            <div key={it.id} className="libtrow" draggable onDragStart={(e) => onDragStart(e, it)} title={t("lib.dragHint")}>
              <span className={"libic f-" + it.folder}>
                <ItemIcon it={it} size={14} />
              </span>
              <button type="button" className="g" onClick={() => openLibrary(it.id)}>
                <b>{it.title || it.name}</b>
                {it.snippet ? <Snippet text={it.snippet} /> : <span>{sourceLine(it, agentName) + " · " + ago(it.updated)}</span>}
              </button>
              <button
                className="ix"
                type="button"
                disabled={!target}
                aria-label={t("lib.attach")}
                title={target ? t("lib.useIn", { agent: target.agent.name }) : t("lib.useNoTarget")}
                onClick={() => target && attachTo(target, it)}
              >
                <Paperclip size={13} />
              </button>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
