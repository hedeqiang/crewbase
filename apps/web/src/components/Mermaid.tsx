// ```mermaid blocks drawn as diagrams. Mermaid is large, so it is loaded
// the first time a diagram is on screen; its output is SVG made with
// securityLevel "strict" (no scripts, no click handlers, HTML labels
// sanitized), then passed through DOMPurify's SVG profile before it is
// put in the page (a second line, should Mermaid let something through).
// Source and diagram can be switched, the diagram opens
// full size, and while an answer is still streaming the source shows
// until the block is complete.

import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Code2, Download, Maximize2, Network, X } from "lucide-react";
import { t } from "../i18n";
import { toast } from "../store";
import { Popover, useMenu } from "./Popover";

type MermaidAPI = typeof import("mermaid").default;
let loading: Promise<MermaidAPI> | null = null;
let themeOf = "";

function isDark(): boolean {
  const th = document.documentElement.dataset.theme;
  return th ? th === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
}

type Purify = typeof import("dompurify").default;
let purifying: Promise<Purify> | null = null;

/** The SVG Mermaid made, with anything that is not drawing removed:
 *  scripts, event handlers, foreignObject (labels are SVG text here),
 *  links out and external references. Its <style> (the theme) stays. */
async function clean(svg: string): Promise<string> {
  purifying ??= import("dompurify").then((m) => m.default);
  const p = await purifying;
  return p.sanitize(svg, {
    USE_PROFILES: { svg: true, svgFilters: true },
    ADD_TAGS: ["style"],
    FORBID_TAGS: ["foreignObject", "script", "a", "image", "use"],
    FORBID_ATTR: ["href", "xlink:href"],
  });
}

async function mermaid(): Promise<MermaidAPI> {
  loading ??= import("mermaid").then((m) => m.default);
  const m = await loading;
  const theme = isDark() ? "dark" : "default";
  if (theme !== themeOf) {
    m.initialize({
      startOnLoad: false,
      securityLevel: "strict",
      theme,
      fontFamily: getComputedStyle(document.body).fontFamily,
      // SVG text labels, not HTML in foreignObject: a canvas that drew
      // foreignObject cannot be exported (PNG download) in WebKit
      htmlLabels: false,
      flowchart: { htmlLabels: false, useMaxWidth: true },
      sequence: { useMaxWidth: true },
    });
    themeOf = theme;
  }
  return m;
}

// one diagram at a time: mermaid keeps global state while rendering
let queue: Promise<unknown> = Promise.resolve();

export function MermaidBlock({ code }: { code: string }) {
  const id = "mmd" + useId().replace(/[^a-zA-Z0-9]/g, "");
  const [svg, setSvg] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [source, setSource] = useState(false);
  const [full, setFull] = useState(false);
  const [dark, setDark] = useState(isDark());
  const box = useRef<HTMLDivElement>(null);

  // follow the app's theme
  useEffect(() => {
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const on = () => setDark(isDark());
    mq.addEventListener("change", on);
    const mo = new MutationObserver(on);
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => {
      mq.removeEventListener("change", on);
      mo.disconnect();
    };
  }, []);

  useEffect(() => {
    let live = true;
    const text = code.trim();
    if (!text) return;
    // wait a moment: a streaming answer changes the block on every chunk
    const h = setTimeout(() => {
      queue = queue.then(async () => {
        try {
          const m = await mermaid();
          await m.parse(text);
          const out = await m.render(id + (dark ? "d" : "l"), text);
          const safe = await clean(out.svg);
          if (live) {
            setSvg(safe);
            setErr(null);
          }
        } catch (e: any) {
          if (live) {
            setErr(String(e?.message ?? e).split("\n").slice(0, 3).join("\n"));
            setSvg(null);
          }
        } finally {
          // mermaid leaves a measuring element behind on errors
          document.getElementById("d" + id + (dark ? "d" : "l"))?.remove();
        }
      });
    }, 250);
    return () => {
      live = false;
      clearTimeout(h);
    };
  }, [code, dark]);

  const showSource = source || (!svg && !err);
  return (
    <div className="mmd">
      <div className="mmdbar">
        <span className="codelang">mermaid</span>
        <span className="sp" />
        {svg && (
          <>
            <button type="button" className="codecopy" aria-pressed={source} title={source ? t("mmd.diagram") : t("mmd.source")} onClick={() => setSource((v) => !v)}>
              {source ? <Network size={13} /> : <Code2 size={13} />}
              <span>{source ? t("mmd.diagram") : t("mmd.source")}</span>
            </button>
            {!source && (
              <>
                <DownloadMenu svg={svg} code={code} />
                <button type="button" className="codecopy" title={t("mmd.full")} aria-label={t("mmd.full")} onClick={() => setFull(true)}>
                  <Maximize2 size={13} />
                </button>
              </>
            )}
          </>
        )}
      </div>
      {svg && !source ? (
        <div className="mmdsvg" ref={box} dangerouslySetInnerHTML={{ __html: svg }} />
      ) : showSource ? (
        <pre className="codeblk">
          <code>{code}</code>
        </pre>
      ) : null}
      {err && !svg && (
        <>
          <pre className="codeblk">
            <code>{code}</code>
          </pre>
          <div className="mmderr small">{t("mmd.error", { error: err })}</div>
        </>
      )}
      {full && svg && <MermaidFull svg={svg} code={code} onClose={() => setFull(false)} />}
    </div>
  );
}

function MermaidFull({ svg, code, onClose }: { svg: string; code: string; onClose: () => void }) {
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="mmdfull" role="dialog" aria-modal="true" aria-label={t("mmd.full")}>
        <div className="mmdfullbar">
          <DownloadMenu svg={svg} code={code} big />
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={15} />
          </button>
        </div>
        <div className="mmdsvg big" dangerouslySetInnerHTML={{ __html: svg }} />
      </div>
    </>,
    document.body,
  );
}

// ---------------------------------------------------------------- download

/** 下载: the diagram as PNG (2× for sharp text, on the theme's background)
 *  or SVG. Made in the page; the desktop shell saves it to Downloads. */
function DownloadMenu({ svg, code, big = false }: { svg: string; code: string; big?: boolean }) {
  const m = useMenu();
  const save = async (kind: "png" | "svg") => {
    m.close();
    try {
      const doc = standalone(svg);
      const name = fileName(code) + "." + kind;
      if (kind === "svg") {
        saveBlob(new Blob([doc.text], { type: "image/svg+xml" }), name);
      } else {
        saveBlob(await toPNG(doc), name);
      }
      toast(t("mmd.saved", { name }));
    } catch (e: any) {
      toast(t("mmd.saveFailed", { error: e?.message ?? String(e) }));
    }
  };
  return (
    <>
      <button type="button" className={big ? "btn sm" : "codecopy"} ref={m.ref} onClick={m.toggle} aria-haspopup="menu" aria-expanded={m.open} title={t("mmd.download")} aria-label={t("mmd.download")}>
        <Download size={13} />
        {big && <span>{t("mmd.download")}</span>}
      </button>
      <Popover anchor={m.anchor} open={m.open} onClose={m.close} align="end" width={200}>
        <button className="mi" type="button" onClick={() => void save("png")}>
          <span className="g">
            {t("mmd.png")}
            <span>{t("mmd.pngDesc")}</span>
          </span>
        </button>
        <button className="mi" type="button" onClick={() => void save("svg")}>
          <span className="g">
            {t("mmd.svg")}
            <span>{t("mmd.svgDesc")}</span>
          </span>
        </button>
      </Popover>
    </>
  );
}

/** The diagram as a file on its own: explicit size, the theme's background
 *  and the page's font (an SVG opened elsewhere knows neither). */
function standalone(svg: string): { text: string; width: number; height: number } {
  const doc = new DOMParser().parseFromString(svg, "image/svg+xml");
  const el = doc.documentElement;
  if (el.nodeName.toLowerCase() !== "svg") throw new Error("not an SVG");
  const vb = (el.getAttribute("viewBox") ?? "").split(/[\s,]+/).map(Number);
  let [x, y, width, height] = vb.length === 4 && vb.every((n) => Number.isFinite(n)) ? vb : [0, 0, 0, 0];
  if (!width || !height) {
    width = parseFloat(el.getAttribute("width") ?? "") || 800;
    height = parseFloat(el.getAttribute("height") ?? "") || 600;
  }
  el.setAttribute("xmlns", "http://www.w3.org/2000/svg");
  el.setAttribute("width", String(Math.ceil(width)));
  el.setAttribute("height", String(Math.ceil(height)));
  el.removeAttribute("style"); // max-width: the page's, not the file's
  const bg = doc.createElementNS("http://www.w3.org/2000/svg", "rect");
  bg.setAttribute("x", String(x));
  bg.setAttribute("y", String(y));
  bg.setAttribute("width", String(width));
  bg.setAttribute("height", String(height));
  bg.setAttribute("fill", isDark() ? "#1c1c1c" : "#ffffff");
  el.insertBefore(bg, el.firstChild);
  const font = getComputedStyle(document.body).fontFamily;
  const st = doc.createElementNS("http://www.w3.org/2000/svg", "style");
  st.textContent = `svg { font-family: ${font}; }`;
  el.insertBefore(st, el.firstChild);
  return { text: new XMLSerializer().serializeToString(el), width, height };
}

async function toPNG(d: { text: string; width: number; height: number }): Promise<Blob> {
  const scale = 2;
  const img = new Image();
  img.decoding = "sync";
  const url = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(d.text);
  await new Promise<void>((ok, fail) => {
    img.onload = () => ok();
    img.onerror = () => fail(new Error("the diagram could not be drawn"));
    img.src = url;
  });
  const c = document.createElement("canvas");
  c.width = Math.ceil(d.width * scale);
  c.height = Math.ceil(d.height * scale);
  const g = c.getContext("2d");
  if (!g) throw new Error("no canvas");
  g.drawImage(img, 0, 0, c.width, c.height);
  return await new Promise<Blob>((ok, fail) => c.toBlob((b) => (b ? ok(b) : fail(new Error("PNG export failed"))), "image/png"));
}

function saveBlob(b: Blob, name: string) {
  const url = URL.createObjectURL(b);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}

/** mermaid-<kind>-<yyyymmdd-hhmm>: the diagram's type from its first line. */
function fileName(code: string): string {
  const kind = (code.trim().split(/\s+/)[0] || "diagram").replace(/[^a-zA-Z0-9-]/g, "").toLowerCase() || "diagram";
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  return `mermaid-${kind}-${d.getFullYear()}${p(d.getMonth() + 1)}${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}`;
}
