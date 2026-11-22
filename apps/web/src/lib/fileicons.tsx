// File-type badges for the 文件 panel: a small coloured tile with the
// type's short name (TS, GO, {}, MD, …), so a tree reads at a glance.
// Pictures, video, folders and plain files keep a drawn icon.

import { File, FileArchive, FileImage, FileLock, FileText, FileVideo, Folder, FolderOpen } from "lucide-react";
import type { CSSProperties } from "react";

type Badge = { label: string; bg: string; fg?: string };

// colours after the languages' own marks, muted to sit in a list
const BY_EXT: Record<string, Badge> = {
  ts: { label: "TS", bg: "#3178c6" },
  tsx: { label: "TSX", bg: "#3178c6" },
  mts: { label: "TS", bg: "#3178c6" },
  js: { label: "JS", bg: "#e8b80e", fg: "#2b2400" },
  mjs: { label: "JS", bg: "#e8b80e", fg: "#2b2400" },
  cjs: { label: "JS", bg: "#e8b80e", fg: "#2b2400" },
  jsx: { label: "JSX", bg: "#e8b80e", fg: "#2b2400" },
  go: { label: "GO", bg: "#00a7d0" },
  py: { label: "PY", bg: "#3572a5" },
  rs: { label: "RS", bg: "#c46a3a" },
  rb: { label: "RB", bg: "#b8312f" },
  java: { label: "JV", bg: "#b07219" },
  kt: { label: "KT", bg: "#7f52ff" },
  swift: { label: "SW", bg: "#f05138" },
  php: { label: "PHP", bg: "#777bb3" },
  c: { label: "C", bg: "#555f9c" },
  h: { label: "H", bg: "#555f9c" },
  cpp: { label: "C++", bg: "#c44d7c" },
  cc: { label: "C++", bg: "#c44d7c" },
  hpp: { label: "H++", bg: "#c44d7c" },
  cs: { label: "C#", bg: "#178600" },
  sh: { label: "$", bg: "#3c873a" },
  bash: { label: "$", bg: "#3c873a" },
  zsh: { label: "$", bg: "#3c873a" },
  json: { label: "{}", bg: "#c9a227", fg: "#2b2400" },
  jsonc: { label: "{}", bg: "#c9a227", fg: "#2b2400" },
  yaml: { label: "YML", bg: "#cb4b6d" },
  yml: { label: "YML", bg: "#cb4b6d" },
  toml: { label: "TML", bg: "#9c4221" },
  ini: { label: "INI", bg: "#7a7a85" },
  env: { label: "ENV", bg: "#7a7a85" },
  xml: { label: "<>", bg: "#0060ac" },
  html: { label: "<>", bg: "#e34c26" },
  htm: { label: "<>", bg: "#e34c26" },
  vue: { label: "VUE", bg: "#41b883" },
  svelte: { label: "SV", bg: "#ff3e00" },
  css: { label: "#", bg: "#663399" },
  scss: { label: "#", bg: "#c6538c" },
  less: { label: "#", bg: "#1d365d" },
  md: { label: "MD", bg: "#4a7eb3" },
  mdx: { label: "MDX", bg: "#4a7eb3" },
  sql: { label: "SQL", bg: "#d4822a" },
  csv: { label: "CSV", bg: "#1f8a4c" },
  tsv: { label: "TSV", bg: "#1f8a4c" },
  xlsx: { label: "XLS", bg: "#1f7244" },
  xls: { label: "XLS", bg: "#1f7244" },
  docx: { label: "DOC", bg: "#2b579a" },
  doc: { label: "DOC", bg: "#2b579a" },
  pptx: { label: "PPT", bg: "#c43e1c" },
  pdf: { label: "PDF", bg: "#d93025" },
  txt: { label: "TXT", bg: "#8a8a93" },
  log: { label: "LOG", bg: "#8a8a93" },
  diff: { label: "±", bg: "#57606a" },
  patch: { label: "±", bg: "#57606a" },
  lock: { label: "LCK", bg: "#8a8a93" },
};

const BY_NAME: Record<string, Badge> = {
  dockerfile: { label: "DK", bg: "#2496ed" },
  makefile: { label: "MK", bg: "#6d6d6d" },
  ".gitignore": { label: "GIT", bg: "#f05032" },
  ".gitattributes": { label: "GIT", bg: "#f05032" },
  ".env": { label: "ENV", bg: "#7a7a85" },
  "package.json": { label: "NPM", bg: "#cb3837" },
  "go.mod": { label: "GO", bg: "#00a7d0" },
  "go.sum": { label: "GO", bg: "#00a7d0" },
  "readme.md": { label: "i", bg: "#4a7eb3" },
  "license": { label: "©", bg: "#8a8a93" },
};

const IMAGE = /\.(png|jpe?g|gif|webp|svg|ico|bmp|heic|avif)$/i;
const VIDEO = /\.(mp4|webm|mov|m4v)$/i;
const ARCHIVE = /\.(zip|gz|tgz|tar|rar|7z)$/i;

function badgeOf(name: string): Badge | null {
  const base = name.toLowerCase();
  if (BY_NAME[base]) return BY_NAME[base];
  if (base.startsWith(".env")) return BY_NAME[".env"];
  const ext = base.includes(".") ? base.split(".").pop()! : "";
  return BY_EXT[ext] ?? null;
}

/** The language a person would name for a file ("TypeScript"), or "". */
export function kindName(name: string): string {
  const b = badgeOf(name);
  const ext = name.toLowerCase().split(".").pop() ?? "";
  const names: Record<string, string> = {
    ts: "TypeScript", tsx: "TypeScript JSX", js: "JavaScript", jsx: "JavaScript JSX", mjs: "JavaScript", go: "Go", py: "Python", rs: "Rust", rb: "Ruby",
    java: "Java", kt: "Kotlin", swift: "Swift", php: "PHP", c: "C", h: "C", cpp: "C++", cs: "C#", sh: "Shell", bash: "Shell", zsh: "Shell",
    json: "JSON", yaml: "YAML", yml: "YAML", toml: "TOML", html: "HTML", css: "CSS", scss: "SCSS", md: "Markdown", sql: "SQL", csv: "CSV",
    xml: "XML", vue: "Vue", txt: "Text", pdf: "PDF",
  };
  if (names[ext]) return names[ext];
  if (b?.label === "DK") return "Dockerfile";
  return "";
}

/** A file's badge (or icon), 16px tall. */
export function FileBadge({ name, dir, open, size = 16 }: { name: string; dir?: boolean; open?: boolean; size?: number }) {
  if (dir) return open ? <FolderOpen size={size - 1} className="fbic dir" /> : <Folder size={size - 1} className="fbic dir" />;
  if (IMAGE.test(name)) return <FileImage size={size - 1} className="fbic img" />;
  if (VIDEO.test(name)) return <FileVideo size={size - 1} className="fbic vid" />;
  if (ARCHIVE.test(name)) return <FileArchive size={size - 1} className="fbic" />;
  const b = badgeOf(name);
  if (!b) return /\.(lock|key|pem)$/i.test(name) ? <FileLock size={size - 1} className="fbic" /> : /\.\w+$/.test(name) ? <File size={size - 1} className="fbic" /> : <FileText size={size - 1} className="fbic" />;
  const style: CSSProperties = { background: b.bg, color: b.fg ?? "#fff", height: size - 2, minWidth: size - 2, fontSize: b.label.length >= 3 ? 7 : b.label.length === 2 ? 8 : 9.5 };
  return (
    <span className="fbadge" style={style} aria-hidden>
      {b.label}
    </span>
  );
}
