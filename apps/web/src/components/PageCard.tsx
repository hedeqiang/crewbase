import { useEffect, useRef, useState } from "react";
import { ExternalLink, Maximize2, PanelRight, RotateCw } from "lucide-react";
import { API, type PageShown } from "../api";
import { t } from "../i18n";
import { openPreview } from "../store";

/* A visual reply (show_page, features/preview/page.go): the page in the
 * conversation, sandboxed on its own origin like every preview. The
 * address recorded may be stale (servers stop when idle), so the card asks
 * for the path again when it comes into view. */

const SANDBOX = "allow-scripts allow-same-origin allow-forms allow-popups allow-modals allow-downloads";

export function PageCard({ p, session }: { p: PageShown; session: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [seen, setSeen] = useState(false);
  const [url, setUrl] = useState("");
  const [err, setErr] = useState("");
  const [tick, setTick] = useState(0);
  const [tall, setTall] = useState(false);

  // load only once it is near the screen (a long conversation may have many)
  useEffect(() => {
    const el = ref.current;
    if (!el || seen) return;
    const io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && setSeen(true), { rootMargin: "400px" });
    io.observe(el);
    return () => io.disconnect();
  }, [seen]);
  useEffect(() => {
    if (!seen || !p.agent) return;
    API.openPreview(p.agent, p.path).then(
      (o) => setUrl(o.url),
      (e) => setErr(e?.message ?? String(e)),
    );
  }, [seen, p.agent, p.path]);

  const toPanel = () =>
    openPreview(session, { title: p.title, url: url || p.url, kind: "path", path: p.path, agent: p.agent, source: "page", call: p.call, time: new Date().toISOString() });

  return (
    <div className={"pagecard" + (tall ? " tall" : "")} ref={ref}>
      <div className="pch">
        <span className="pct" title={p.path}>
          {p.title}
        </span>
        <span className="sp" />
        <button type="button" className="ix" title={t("page.reload")} aria-label={t("page.reload")} onClick={() => setTick(tick + 1)} disabled={!url}>
          <RotateCw size={13} />
        </button>
        <button type="button" className="ix" title={t(tall ? "page.shorter" : "page.taller")} aria-label={t(tall ? "page.shorter" : "page.taller")} onClick={() => setTall(!tall)}>
          <Maximize2 size={13} />
        </button>
        <button type="button" className="ix" title={t("page.panel")} aria-label={t("page.panel")} onClick={toPanel}>
          <PanelRight size={13} />
        </button>
        <a className="ix" href={url || undefined} target="_blank" rel="noreferrer noopener" title={t("page.browser")} aria-label={t("page.browser")}>
          <ExternalLink size={13} />
        </a>
      </div>
      <div className="pcb">
        {err ? (
          <div className="pcmsg small errt">{t("page.gone", { error: err })}</div>
        ) : url ? (
          <iframe key={tick} src={url} title={p.title} sandbox={SANDBOX} referrerPolicy="no-referrer" allow="clipboard-write; fullscreen" />
        ) : (
          <div className="pcmsg small muted">{t("common.loading")}</div>
        )}
      </div>
      {p.notes && p.notes.length > 0 && (
        <div className="pcnote small muted" title={p.notes.join("\n")}>
          {t("page.notes", { n: p.notes.length })}
        </div>
      )}
    </div>
  );
}
