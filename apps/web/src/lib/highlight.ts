// Syntax highlighting for the 文件 panel (code view and diffs). Only the
// common languages are bundled; anything else shows as plain text.
import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import go from "highlight.js/lib/languages/go";
import ini from "highlight.js/lib/languages/ini";
import java from "highlight.js/lib/languages/java";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import kotlin from "highlight.js/lib/languages/kotlin";
import markdown from "highlight.js/lib/languages/markdown";
import php from "highlight.js/lib/languages/php";
import python from "highlight.js/lib/languages/python";
import ruby from "highlight.js/lib/languages/ruby";
import rust from "highlight.js/lib/languages/rust";
import sql from "highlight.js/lib/languages/sql";
import swift from "highlight.js/lib/languages/swift";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";
import c from "highlight.js/lib/languages/c";
import cpp from "highlight.js/lib/languages/cpp";
import csharp from "highlight.js/lib/languages/csharp";
import dockerfile from "highlight.js/lib/languages/dockerfile";

for (const [n, l] of Object.entries({ bash, css, diff, go, ini, java, javascript, json, kotlin, markdown, php, python, ruby, rust, sql, swift, typescript, xml, yaml, c, cpp, csharp, dockerfile })) {
  hljs.registerLanguage(n, l);
}

const BY_EXT: Record<string, string> = {
  js: "javascript", mjs: "javascript", cjs: "javascript", jsx: "javascript",
  ts: "typescript", tsx: "typescript", mts: "typescript",
  go: "go", py: "python", rb: "ruby", rs: "rust", java: "java", kt: "kotlin", kts: "kotlin", swift: "swift",
  php: "php", c: "c", h: "c", cc: "cpp", cpp: "cpp", hpp: "cpp", cs: "csharp",
  sh: "bash", bash: "bash", zsh: "bash",
  json: "json", jsonc: "json", yaml: "yaml", yml: "yaml", toml: "ini", ini: "ini", conf: "ini", env: "ini",
  html: "xml", htm: "xml", xml: "xml", svg: "xml", vue: "xml",
  css: "css", scss: "css", less: "css",
  md: "markdown", markdown: "markdown", sql: "sql", diff: "diff", patch: "diff",
};

/** The language of a file by its name ("" when not known). */
export function langOf(path: string): string {
  const base = path.split("/").pop() ?? path;
  if (/^dockerfile$/i.test(base)) return "dockerfile";
  if (/^\.(env|gitignore|npmrc|editorconfig)/.test(base)) return "ini";
  const ext = base.includes(".") ? base.split(".").pop()!.toLowerCase() : "";
  return BY_EXT[ext] ?? "";
}

const escapeHTML = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

/** Highlights code as HTML, one string per line (spans never cross lines,
 *  so lines can be laid out and numbered one by one). */
export function highlightLines(code: string, lang: string): string[] {
  if (!lang || !hljs.getLanguage(lang) || code.length > 400_000) return code.split("\n").map(escapeHTML);
  let html: string;
  try {
    html = hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
  } catch {
    return code.split("\n").map(escapeHTML);
  }
  // reopen the spans still open at each line break
  const out: string[] = [];
  const open: string[] = [];
  let cur = "";
  const re = /(<span[^>]*>)|(<\/span>)|(\n)|([^<\n]+|<)/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(html))) {
    if (m[1]) {
      open.push(m[1]);
      cur += m[1];
    } else if (m[2]) {
      open.pop();
      cur += m[2];
    } else if (m[3]) {
      out.push(cur + "</span>".repeat(open.length));
      cur = open.join("");
    } else cur += m[4];
  }
  out.push(cur + "</span>".repeat(open.length));
  return out;
}
