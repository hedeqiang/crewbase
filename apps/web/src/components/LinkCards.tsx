import { useEffect, useState } from "react";
import { Play } from "lucide-react";
import { API, type LinkCard } from "../api";
import { t } from "../i18n";
import { useStore } from "../store";

// Link cards (docs/previews.md, part 一): the first three http(s) links of
// a message, unfurled by the daemon (GET /api/unfurl). Links in code are
// skipped, and so are localhost / LAN addresses (the daemon would refuse
// them anyway; localhost belongs to the 预览 panel). A link that cannot be
// fetched shows nothing.

const MAX = 3; // cards shown
const TRY = 8; // links fetched to find them (dead links take no slot)
const LINK = /\[[^\]\n]+\]\((https?:\/\/[^)\s]+)\)|(https?:\/\/[^\s<>()　-〿＀-￯]+[^\s<>().,;:!?'"　-〿＀-￯])/g;

function localHost(h: string): boolean {
  h = h.toLowerCase().replace(/^\[|\]$/g, "").replace(/\.$/, "");
  if (h === "localhost" || h.endsWith(".localhost") || h.endsWith(".local") || h.endsWith(".internal")) return true;
  if (/^(127|10|0)\./.test(h) || /^192\.168\./.test(h) || /^169\.254\./.test(h) || /^172\.(1[6-9]|2\d|3[01])\./.test(h)) return true;
  return h.includes(":") && (h === "::1" || h === "::" || /^f[cde]/.test(h) || h.startsWith("::ffff:"));
}

/** The links worth a card, in order, without duplicates. */
export function cardLinks(text: string): string[] {
  let fence = "";
  const kept: string[] = [];
  for (const line of text.split("\n")) {
    const f = /^\s*(`{3,}|~{3,})/.exec(line)?.[1];
    if (fence) {
      if (f && f[0] === fence[0] && f.length >= fence.length) fence = "";
    } else if (f) fence = f;
    else kept.push(line);
  }
  const prose = kept.join("\n").replace(/`[^`\n]+`/g, " "); // inline code
  const out: string[] = [];
  for (const m of prose.matchAll(LINK)) {
    const raw = m[1] ?? m[2];
    let u: URL;
    try {
      u = new URL(raw);
    } catch {
      continue;
    }
    if (localHost(u.hostname)) continue;
    u.hash = "";
    const k = u.toString();
    if (!out.includes(k)) out.push(k);
    if (out.length >= TRY) break;
  }
  return out;
}

// per page load: one request per link, however often messages re-render
const cache = new Map<string, Promise<LinkCard | null>>();
function unfurl(url: string): Promise<LinkCard | null> {
  let p = cache.get(url);
  if (!p) {
    p = API.unfurl(url).catch(() => null);
    cache.set(url, p);
  }
  return p;
}

/** exclude: text whose links already have cards (an answer repeating the
 *  links of the message it answers shows no second card). */
export function LinkCards({ text, mine, exclude }: { text: string; mine?: boolean; exclude?: string }) {
  const on = useStore((s) => s.linkCards);
  if (!on) return null;
  const seen = exclude ? new Set(cardLinks(exclude)) : null;
  const links = cardLinks(text).filter((u) => !seen?.has(u));
  if (!links.length) return null;
  return <Cards links={links} mine={mine} />;
}

/** The first MAX links that unfurl, in text order: a card shows once every
 *  link before it has answered, so cards never jump. */
function Cards({ links, mine }: { links: string[]; mine?: boolean }) {
  const [got, setGot] = useState<Record<string, LinkCard | null>>({});
  const key = links.join(" ");
  useEffect(() => {
    let live = true;
    for (const u of links) void unfurl(u).then((x) => live && setGot((g) => ({ ...g, [u]: x })));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  const show: LinkCard[] = [];
  for (const u of links) {
    if (!(u in got)) break;
    const c = got[u];
    if (c) show.push(c);
    if (show.length >= MAX) break;
  }
  if (!show.length) return null;
  return (
    <div className={"lcards" + (mine ? " mine" : "")}>
      {show.map((c, i) => (
        <OneCard key={c.url + i} c={c} />
      ))}
    </div>
  );
}

function OneCard({ c }: { c: LinkCard }) {
  if (c.kind === "image" && c.media) return <ImageCard c={c} />;
  if (c.kind === "video" && c.embed) return <EmbedCard c={c} />;
  if (c.kind === "video" && c.media)
    return (
      <div className="lcard lmedia">
        <video controls preload="metadata" src={c.media} />
      </div>
    );
  if (c.kind === "audio" && c.media)
    return (
      <div className="lcard laudio">
        <span className="lsite">{c.title || c.siteName}</span>
        <audio controls preload="metadata" src={c.media} />
      </div>
    );
  return <PageCard c={c} />;
}

function Img({ src, className, alt = "", onFail }: { src?: string; className?: string; alt?: string; onFail?: () => void }) {
  const [bad, setBad] = useState(false);
  if (!src || bad) return null;
  return (
    <img
      className={className}
      src={src}
      alt={alt}
      loading="lazy"
      referrerPolicy="no-referrer"
      onError={() => {
        setBad(true);
        onFail?.();
      }}
    />
  );
}

function Site({ c }: { c: LinkCard }) {
  return (
    <span className="lsite">
      <Img className="lico" src={c.icon} />
      <span>{c.siteName}</span>
    </span>
  );
}

function PageCard({ c }: { c: LinkCard }) {
  return (
    <a className="lcard lpage" href={c.url} target="_blank" rel="noreferrer noopener" title={c.url}>
      <span className="ltext">
        <Site c={c} />
        {c.title && <span className="ltitle">{c.title}</span>}
        {c.description && <span className="ldesc">{c.description}</span>}
      </span>
      <Img className="lthumb" src={c.image} />
    </a>
  );
}

function ImageCard({ c }: { c: LinkCard }) {
  const [bad, setBad] = useState(false);
  if (bad) return <PageCard c={{ ...c, title: c.title || c.url }} />;
  return (
    <a className="lcard limg" href={c.media} target="_blank" rel="noreferrer noopener" title={c.url}>
      <Img src={c.media} alt={c.title} onFail={() => setBad(true)} />
    </a>
  );
}

/** The player, started by the click that opened it. Not YouTube: its embed
 *  answers autoplay with a "confirm you're not a bot" wall (seen headless,
 *  through a proxy), so there the player's own play button is one more click. */
function playSrc(e: NonNullable<LinkCard["embed"]>): string {
  if (e.provider === "youtube") return e.src;
  if (/autoplay=0/.test(e.src)) return e.src.replace("autoplay=0", "autoplay=1");
  return e.src + (e.src.includes("?") ? "&" : "?") + "autoplay=1";
}

function EmbedCard({ c }: { c: LinkCard }) {
  const [playing, setPlaying] = useState(false);
  const [noThumb, setNoThumb] = useState(!c.image);
  const e = c.embed!;
  const title = c.title || t("link.video");
  return (
    <div className="lcard lvideo">
      <div className="lplayer" style={{ aspectRatio: String(e.aspect || 16 / 9) }}>
        {playing ? (
          <iframe
            src={playSrc(e)}
            title={title}
            allow="autoplay; encrypted-media; picture-in-picture; fullscreen"
            allowFullScreen
            referrerPolicy="strict-origin-when-cross-origin"
          />
        ) : (
          <button type="button" className={"lposter" + (noThumb ? " blank" : "")} aria-label={t("link.play", { title })} onClick={() => setPlaying(true)}>
            <Img src={c.image} onFail={() => setNoThumb(true)} />
            <span className="lplay">
              <Play size={22} fill="currentColor" />
            </span>
          </button>
        )}
      </div>
      <a className="ltext" href={c.url} target="_blank" rel="noreferrer noopener" title={c.url}>
        <Site c={c} />
        <span className="ltitle">{title}</span>
      </a>
    </div>
  );
}
