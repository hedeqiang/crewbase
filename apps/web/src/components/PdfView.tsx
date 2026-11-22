// A PDF shown in the page (pdf.js): pages render as they scroll into view,
// text can be selected and copied, zoom fits the width by default. The
// server never serves a PDF inline; this reads its bytes and draws them.

import { useEffect, useMemo, useRef, useState } from "react";
import { Maximize2, Minus, Plus } from "lucide-react";
import { useRawURL } from "../api";
import { t } from "../i18n";
import type { PDFDocumentProxy, PDFPageProxy } from "../lib/pdf";

type Lib = typeof import("../lib/pdf");
let lib: Promise<Lib> | null = null;
const loadLib = () => (lib ??= import("../lib/pdf"));

const ZOOMS = [0.5, 0.67, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3];

export function PdfView({ agent, path }: { agent: string; path: string }) {
  const src = useRawURL(agent, path);
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null);
  const [sizes, setSizes] = useState<{ w: number; h: number }[]>([]);
  const [err, setErr] = useState("");
  const [zoom, setZoom] = useState<number | "fit">("fit");
  const [width, setWidth] = useState(0);
  const [page, setPage] = useState(1);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!src) return;
    let gone = false;
    let opened: PDFDocumentProxy | null = null;
    setDoc(null);
    setErr("");
    (async () => {
      const [{ openPdf }, res] = await Promise.all([loadLib(), fetch(src)]);
      if (!res.ok) throw new Error(res.status === 400 ? t("pdf.tooLarge") : `HTTP ${res.status}`);
      const d = await openPdf(new Uint8Array(await res.arrayBuffer()));
      opened = d;
      // every page's size first, so the pages hold their place while they render
      const ss: { w: number; h: number }[] = [];
      for (let i = 1; i <= d.numPages; i++) {
        const v = (await d.getPage(i)).getViewport({ scale: 1 });
        ss.push({ w: v.width, h: v.height });
      }
      if (gone) return void d.loadingTask.destroy();
      setSizes(ss);
      setDoc(d);
    })().catch((e) => !gone && setErr(e?.name === "PasswordException" ? t("pdf.locked") : e?.message ?? String(e)));
    return () => {
      gone = true;
      void opened?.loadingTask.destroy();
    };
  }, [src]);

  // the width to fit
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, [doc]);

  const widest = useMemo(() => Math.max(1, ...sizes.map((s) => s.w)), [sizes]);
  const fit = width > 0 ? Math.max(0.25, (width - 32) / widest) : 1;
  const scale = zoom === "fit" ? fit : zoom;
  const pct = Math.round(scale * 100);

  // which page is in view
  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    const mid = el.scrollTop + el.clientHeight / 3;
    let y = 16;
    for (let i = 0; i < sizes.length; i++) {
      y += sizes[i].h * scale + 12;
      if (y > mid) {
        if (page !== i + 1) setPage(i + 1);
        return;
      }
    }
  };
  const step = (d: 1 | -1) => {
    const cur = scale;
    const next = d > 0 ? ZOOMS.find((z) => z > cur + 0.01) : [...ZOOMS].reverse().find((z) => z < cur - 0.01);
    if (next) setZoom(next);
  };
  // ⌘ + / − / 0 while the PDF is focused or hovered
  const keys = (e: React.KeyboardEvent) => {
    if (!(e.metaKey || e.ctrlKey)) return;
    if (e.key === "=" || e.key === "+") (e.preventDefault(), step(1));
    else if (e.key === "-") (e.preventDefault(), step(-1));
    else if (e.key === "0") (e.preventDefault(), setZoom("fit"));
  };

  // many PDFs (Chrome's among them) map CJK characters to their look-alike
  // Kangxi radicals (⽣ for 生): copy the characters people mean
  const copy = (e: React.ClipboardEvent) => {
    const text = window.getSelection()?.toString() ?? "";
    if (!/[\u2E80-\u2FDF]/.test(text)) return;
    e.preventDefault();
    e.clipboardData.setData("text/plain", text.replace(/[\u2E80-\u2FDF]/g, (c) => c.normalize("NFKC")));
  };

  if (err)
    return (
      <div className="fxmsg small">
        {t("pdf.failed")}
        <span className="muted"> · {err}</span>
      </div>
    );
  return (
    <div className="pdfv" onKeyDown={keys}>
      <div className="pdfbar">
        <span className="pdfn">{doc ? t("pdf.page", { i: page, n: doc.numPages }) : t("common.loading")}</span>
        <span className="sp" />
        <button className="ix" type="button" aria-label={t("pdf.zoomOut")} title={t("pdf.zoomOut")} disabled={!doc || scale <= ZOOMS[0]} onClick={() => step(-1)}>
          <Minus size={13} />
        </button>
        <button className="pdfz" type="button" title={t("pdf.fit")} onClick={() => setZoom("fit")} disabled={!doc}>
          {pct}%
        </button>
        <button className="ix" type="button" aria-label={t("pdf.zoomIn")} title={t("pdf.zoomIn")} disabled={!doc || scale >= ZOOMS[ZOOMS.length - 1]} onClick={() => step(1)}>
          <Plus size={13} />
        </button>
        <button className={"ix" + (zoom === "fit" ? " on" : "")} type="button" aria-label={t("pdf.fit")} title={t("pdf.fit")} disabled={!doc} onClick={() => setZoom("fit")}>
          <Maximize2 size={12} />
        </button>
      </div>
      <div className="pdfpages" ref={box} onScroll={onScroll} tabIndex={0} onCopy={copy}>
        {!doc ? (
          <div className="pdfph" />
        ) : (
          sizes.map((s, i) => <Page key={i} doc={doc} n={i + 1} w={s.w} h={s.h} scale={scale} root={box} />)
        )}
      </div>
    </div>
  );
}

/** One page: drawn (canvas + text layer) once it is near the view, again
 *  when the zoom changes. */
function Page({ doc, n, w, h, scale, root }: { doc: PDFDocumentProxy; n: number; w: number; h: number; scale: number; root: React.RefObject<HTMLDivElement | null> }) {
  const el = useRef<HTMLDivElement>(null);
  const [near, setNear] = useState(n <= 2);
  useEffect(() => {
    const node = el.current;
    if (!node || !root.current) return;
    const io = new IntersectionObserver((es) => es[0]?.isIntersecting && setNear(true), { root: root.current, rootMargin: "800px 0px" });
    io.observe(node);
    return () => io.disconnect();
  }, []);
  useEffect(() => {
    if (!near) return;
    const node = el.current;
    if (!node) return;
    let task: { cancel: () => void } | null = null;
    let text: { cancel: () => void } | null = null;
    let gone = false;
    (async () => {
      const { TextLayer } = await import("../lib/pdf");
      const p: PDFPageProxy = await doc.getPage(n);
      if (gone) return;
      const vp = p.getViewport({ scale });
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      const canvas = document.createElement("canvas");
      canvas.width = Math.floor(vp.width * dpr);
      canvas.height = Math.floor(vp.height * dpr);
      const r = p.render({ canvas, canvasContext: canvas.getContext("2d")!, viewport: vp, transform: dpr !== 1 ? [dpr, 0, 0, dpr, 0, 0] : undefined });
      task = r;
      await r.promise;
      if (gone) return;
      const layer = document.createElement("div");
      layer.className = "textLayer";
      const tl = new TextLayer({ textContentSource: p.streamTextContent(), container: layer, viewport: vp });
      text = tl;
      // the new drawing replaces the old one only when it is ready (no flash on zoom)
      node.querySelector("canvas")?.remove();
      node.querySelector(".textLayer")?.remove();
      node.append(canvas, layer);
      await tl.render().catch(() => {});
    })().catch(() => {});
    return () => {
      gone = true;
      task?.cancel();
      text?.cancel();
    };
  }, [near, scale]);
  return (
    <div
      ref={el}
      className="pdfpage"
      data-page={n}
      style={{ width: Math.floor(w * scale), height: Math.floor(h * scale), "--total-scale-factor": scale, "--scale-round-x": "1px", "--scale-round-y": "1px" } as React.CSSProperties}
      aria-label={t("pdf.pageN", { n })}
    />
  );
}
