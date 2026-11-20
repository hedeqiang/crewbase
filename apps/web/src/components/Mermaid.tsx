// ```mermaid blocks drawn as diagrams. Mermaid is large, so it is loaded
// the first time a diagram is on screen; its output is SVG made with
// securityLevel "strict" (no scripts, no click handlers, HTML labels
// sanitized). Source and diagram can be switched, the diagram opens
// full size, and while an answer is still streaming the source shows
// until the block is complete.

import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Code2, Maximize2, Network, X } from "lucide-react";
import { t } from "../i18n";

type MermaidAPI = typeof import("mermaid").default;
let loading: Promise<MermaidAPI> | null = null;
let themeOf = "";

function isDark(): boolean {
  const th = document.documentElement.dataset.theme;
  return th ? th === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
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
      flowchart: { htmlLabels: true, useMaxWidth: true },
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
          if (live) {
            setSvg(out.svg);
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
              <button type="button" className="codecopy" title={t("mmd.full")} aria-label={t("mmd.full")} onClick={() => setFull(true)}>
                <Maximize2 size={13} />
              </button>
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
      {full && svg && <MermaidFull svg={svg} onClose={() => setFull(false)} />}
    </div>
  );
}

function MermaidFull({ svg, onClose }: { svg: string; onClose: () => void }) {
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="mmdfull" role="dialog" aria-modal="true" aria-label={t("mmd.full")}>
        <button className="ix mmdclose" type="button" aria-label={t("common.close")} onClick={onClose}>
          <X size={15} />
        </button>
        <div className="mmdsvg big" dangerouslySetInnerHTML={{ __html: svg }} />
      </div>
    </>,
    document.body,
  );
}
