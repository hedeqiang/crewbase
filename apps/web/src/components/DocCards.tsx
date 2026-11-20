// Cards for links to Open Agent Docs pages in an answer: the page's title
// and its first blocks, read through Crewbase (the docs' HTML is not put in
// the app: the Markdown is drawn by our own renderer), with 在右侧打开.

import { useEffect, useState } from "react";
import { ExternalLink, FileText, PanelRight, RefreshCw } from "lucide-react";
import { API, openExternal, type DocPageCard } from "../api";
import { t } from "../i18n";
import { ago } from "../format";
import { docsEmbeddable, isDocsLink, openDocs, useStore } from "../store";
import { Markdown } from "./Markdown";

const URL_RE = /https?:\/\/[^\s<>()`'"\]]+/g;
const MAX = 3;

/** Links to pages of the connected docs server in text (outside code). */
export function docPageLinks(text: string): string[] {
  const prose = text.replace(/(```|~~~)[\s\S]*?(\1|$)/g, " ").replace(/`[^`\n]*`/g, " ");
  const out: string[] = [];
  for (const m of prose.matchAll(URL_RE)) {
    const u = m[0].replace(/[.,;:!?)\]，。；：！？）]+$/, "");
    if (!isDocsLink(u)) continue;
    try {
      const p = new URL(u);
      if (!/^\/p\/[\w-]+\/?$/.test(p.pathname)) continue;
      const key = p.origin + p.pathname.replace(/\/$/, "");
      if (!out.includes(key)) out.push(key);
    } catch {
      continue;
    }
    if (out.length === MAX) break;
  }
  return out;
}

// a card read a moment ago is not read again (several answers link one page)
const cache = new Map<string, { at: number; card: DocPageCard | null; err?: string }>();

export function DocCards({ text }: { text: string }) {
  const connected = useStore((s) => !!s.agentDocs);
  if (!connected) return null;
  const links = docPageLinks(text);
  if (!links.length) return null;
  return (
    <div className="doccards">
      {links.map((u) => (
        <DocCard key={u} url={u} />
      ))}
    </div>
  );
}

function DocCard({ url }: { url: string }) {
  const [state, setState] = useState(() => cache.get(url) ?? null);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const c = cache.get(url);
    if (c && Date.now() - c.at < 15_000 && tick === 0) {
      setState(c);
      return;
    }
    let live = true;
    API.agentDocsPage(url).then(
      (card) => {
        const v = { at: Date.now(), card };
        cache.set(url, v);
        if (live) setState(v);
      },
      (e) => {
        const v = { at: Date.now(), card: null, err: e?.status === 404 ? t("dcard.gone") : (e?.message ?? String(e)) };
        cache.set(url, v);
        if (live) setState(v);
      },
    );
    return () => {
      live = false;
    };
  }, [url, tick]);

  const open = () => (docsEmbeddable() ? openDocs(url) : openExternal(url));
  const card = state?.card;
  if (state && !card)
    return (
      <div className="doccard bad">
        <FileText size={14} />
        <span className="small muted">{state.err}</span>
        <span className="sp" />
        <button className="ix" type="button" title={t("ad.open")} aria-label={t("ad.open")} onClick={() => openExternal(url)}>
          <ExternalLink size={13} />
        </button>
      </div>
    );
  const md = (card?.markdown ?? "").replace(/⟦[^⟧]*⟧\s?/g, "").trim();
  return (
    <div className="doccard">
      <div className="dch">
        <span className="dcic">{card?.icon ? <span className="emoji">{card.icon}</span> : <FileText size={15} />}</span>
        <button type="button" className="dct" onClick={open} title={card?.title}>
          <b>{card ? card.title || t("dcard.untitled") : t("common.loading")}</b>
          <span className="small muted">
            Open Agent Docs{card?.updated ? " · " + t("dcard.updated", { when: ago(new Date(card.updated).toISOString()) }) : ""}
          </span>
        </button>
        <button className="ix" type="button" title={t("dcard.refresh")} aria-label={t("dcard.refresh")} onClick={() => setTick((n) => n + 1)}>
          <RefreshCw size={13} />
        </button>
        <button className="ix" type="button" title={t("ad.open")} aria-label={t("ad.open")} onClick={() => openExternal(url)}>
          <ExternalLink size={13} />
        </button>
      </div>
      {card && (
        <button type="button" className="dcbody" onClick={open} title={t("dcard.openRight")}>
          {md ? (
            <div className="fxmd">
              <Markdown text={md} />
            </div>
          ) : (
            <p className="small muted">{t("dcard.empty")}</p>
          )}
        </button>
      )}
      {card && (
        <div className="dcfoot">
          <button className="btn sm" type="button" onClick={open}>
            <PanelRight size={12} />
            {docsEmbeddable() ? t("dcard.openRight") : t("ad.open")}
          </button>
          {card.total > card.shown && <span className="small muted">{t("dcard.more", { n: card.total - card.shown })}</span>}
        </div>
      )}
    </div>
  );
}
