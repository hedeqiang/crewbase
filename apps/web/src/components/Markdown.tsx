import { Fragment, type ReactNode } from "react";
import { AppWindow, Check, Copy } from "lucide-react";
import { previewSnippet } from "./Preview";
import { useCopy } from "../clipboard";
import { t } from "../i18n";
import { toast } from "../store";

// Markdown-lite: fenced code (with a copy button and the language),
// headings, bullet / numbered / task lists, block quotes, rules, GFM
// tables, paragraphs, and inline `code`, **bold**, *italic*, ~~strike~~,
// [links](url).
// Never uses innerHTML.

/** A fenced code block: the language (when given) and a copy button that
 *  copies the code exactly as written. */
export function CodeBlock({ code, lang }: { code: string; lang?: string }) {
  const { copied, copy } = useCopy();
  const label = copied ? t("msg.copied") : t("msg.copyCode");
  return (
    <div className={"codewrap" + (lang ? " haslang" : "")}>
      {lang && <span className="codelang">{lang}</span>}
      {/^html?$/i.test(lang ?? "") && code.trim() && (
        <button type="button" className="codecopy codeprev" aria-label={t("preview.btn")} title={t("preview.inPanel")} onClick={() => void previewSnippet(code)}>
          <AppWindow size={13} />
          <span>{t("preview.btn")}</span>
        </button>
      )}
      <button type="button" className={"codecopy" + (copied ? " on" : "")} aria-label={label} title={label} onClick={() => void copy(code).then((ok) => ok || toast(t("msg.copyFailed")))}>
        {copied ? <Check size={13} /> : <Copy size={13} />}
        <span>{copied ? t("msg.copied") : t("msg.copy")}</span>
      </button>
      <pre className="codeblk">
        <code>{code}</code>
      </pre>
    </div>
  );
}

function inline(s: string, key = ""): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*)|(\*[^*\n]+\*)|(\[[^\]\n]+\]\((https?:\/\/[^)\s]+)\))|(https?:\/\/[^\s<>()\u3000-\u303f\uff00-\uffef]+[^\s<>().,;:!?'"\u3000-\u303f\uff00-\uffef])|(~~[^~\n]+~~)/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(s))) {
    if (m.index > last) out.push(s.slice(last, m.index));
    const k = `${key}-${i++}`;
    if (m[1]) out.push(<code key={k}>{m[1].slice(1, -1)}</code>);
    else if (m[2]) out.push(<b key={k}>{m[2].slice(2, -2)}</b>);
    else if (m[3]) out.push(<i key={k}>{m[3].slice(1, -1)}</i>);
    else if (m[7]) out.push(<s key={k}>{m[7].slice(2, -2)}</s>);
    else if (m[4]) {
      const text = m[4].slice(1, m[4].indexOf("]"));
      out.push(
        <a key={k} href={m[5]} target="_blank" rel="noreferrer noopener">
          {text}
        </a>,
      );
    } else if (m[6])
      out.push(
        <a key={k} href={m[6]} target="_blank" rel="noreferrer noopener">
          {m[6]}
        </a>,
      );
    last = m.index + m[0].length;
  }
  if (last < s.length) out.push(s.slice(last));
  return out;
}

const isRow = (l: string) => /^\s*\|.*\|\s*$/.test(l);
const isSep = (l: string) => /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(l);

function cells(l: string): string[] {
  return l
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((c) => c.trim());
}

function align(sep: string): ("left" | "center" | "right" | undefined)[] {
  return cells(sep).map((c) => {
    const l = c.startsWith(":"), r = c.endsWith(":");
    return l && r ? "center" : r ? "right" : l ? "left" : undefined;
  });
}

function withBreaks(lines: string[], key: string): ReactNode[] {
  return lines.flatMap((l, i) => (i ? [<br key={`${key}-br${i}`} />, ...inline(l, `${key}-${i}`)] : inline(l, `${key}-${i}`)));
}

export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const blocks: ReactNode[] = [];
  let i = 0;
  let b = 0;
  while (i < lines.length) {
    const line = lines[i];
    const key = "b" + b++;
    const fence = line.match(/^(\s*)(`{3,}|~{3,})\s*([\w+#.-]*)/);
    if (fence) {
      const body: string[] = [];
      const close = new RegExp("^\\s*" + fence[2][0] + "{" + fence[2].length + ",}\\s*$");
      const indent = fence[1].length;
      i++;
      while (i < lines.length && !close.test(lines[i])) {
        const l = lines[i++];
        // a fence inside a list item is indented, and so is its body
        body.push(indent && l.slice(0, indent).trim() === "" ? l.slice(indent) : l);
      }
      i++;
      blocks.push(<CodeBlock key={key} code={body.join("\n")} lang={fence[3] || undefined} />);
      continue;
    }
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) {
      blocks.push(<hr key={key} className="mdhr" />);
      i++;
      continue;
    }
    if (!line.trim()) {
      i++;
      continue;
    }
    if (isRow(line) && i + 1 < lines.length && isSep(lines[i + 1])) {
      const head = cells(line);
      const al = align(lines[i + 1]);
      i += 2;
      const rows: string[][] = [];
      while (i < lines.length && isRow(lines[i])) rows.push(cells(lines[i++]));
      blocks.push(
        <div key={key} className="mdtable">
          <table>
            <thead>
              <tr>
                {head.map((c, j) => (
                  <th key={j} style={{ textAlign: al[j] }}>
                    {inline(c, `${key}-h${j}`)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, ri) => (
                <tr key={ri}>
                  {head.map((_, j) => (
                    <td key={j} style={{ textAlign: al[j] }}>
                      {inline(r[j] ?? "", `${key}-${ri}-${j}`)}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>,
      );
      continue;
    }
    const h = line.match(/^(#{1,6})\s+(.*?)(\s+#+)?\s*$/);
    if (h) {
      blocks.push(
        <div key={key} className={`mdh mdh${Math.min(h[1].length, 4)}`}>
          {inline(h[2], key)}
        </div>,
      );
      i++;
      continue;
    }
    if (/^\s*[-*+]\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*[-*+]\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*[-*+]\s+/, ""));
      blocks.push(
        <ul key={key}>
          {items.map((x, j) => {
            const task = /^\[([ xX])\]\s+/.exec(x);
            if (!task) return <li key={j}>{inline(x, `${key}-${j}`)}</li>;
            return (
              <li key={j} className="task">
                <input type="checkbox" checked={task[1] !== " "} readOnly disabled aria-label={task[1] !== " " ? t("msg.taskDone") : t("msg.taskOpen")} />
                {inline(x.slice(task[0].length), `${key}-${j}`)}
              </li>
            );
          })}
        </ul>,
      );
      continue;
    }
    if (/^\s*\d+[.)]\s+/.test(line)) {
      const items: string[] = [];
      while (i < lines.length && /^\s*\d+[.)]\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*\d+[.)]\s+/, ""));
      blocks.push(
        <ol key={key}>
          {items.map((x, j) => (
            <li key={j}>{inline(x, `${key}-${j}`)}</li>
          ))}
        </ol>,
      );
      continue;
    }
    if (/^\s*>/.test(line)) {
      const q: string[] = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) q.push(lines[i++].replace(/^\s*>\s?/, ""));
      blocks.push(<blockquote key={key}>{withBreaks(q, key)}</blockquote>);
      continue;
    }
    const para: string[] = [];
    while (
      i < lines.length &&
      lines[i].trim() &&
      !/^\s*(```|~~~)/.test(lines[i]) &&
      !/^(#{1,6})\s+/.test(lines[i]) &&
      !/^\s*([-*+]|\d+[.)])\s+/.test(lines[i]) &&
      !/^\s*>/.test(lines[i])
    )
      para.push(lines[i++]);
    blocks.push(<p key={key}>{withBreaks(para, key)}</p>);
  }
  return <Fragment>{blocks}</Fragment>;
}
