// An artifact a turn made or changed with Open Artifact
// (open-artifact/artifact): its type, title and folder; a click shows it
// in the 预览 panel.

import { ChartColumn, Clapperboard, ExternalLink, FileText, Palette, Presentation, Shapes, Sheet, PenTool } from "lucide-react";
import type { ReactNode } from "react";
import { openExternal, type ArtifactCardInfo } from "../api";
import { t, type Key } from "../i18n";
import { openPreview } from "../store";

const ICONS: Record<string, ReactNode> = {
  slides: <Presentation size={16} />,
  sheets: <Sheet size={16} />,
  docs: <FileText size={16} />,
  design: <PenTool size={16} />,
  "design-system": <Palette size={16} />,
  whiteboard: <Shapes size={16} />,
  motion: <Clapperboard size={16} />,
  data: <ChartColumn size={16} />,
};
const KNOWN = new Set(Object.keys(ICONS));

export function ArtifactCard({ a, session }: { a: ArtifactCardInfo; session: string }) {
  const type = a.type && KNOWN.has(a.type) ? a.type : "other";
  const label = t(`oa.t.${type}` as Key);
  const title = a.title || label;
  const show = () => {
    if (a.viewUrl) openPreview(session, { title, url: a.viewUrl, kind: "url", source: "open-artifact", agent: a.agent, time: new Date().toISOString() });
  };
  return (
    <div className="prvcards">
      <div className="prvcard oacard">
        <button className="oamain" type="button" disabled={!a.viewUrl} onClick={show} title={t("oa.cardOpen")}>
          <span className="prvcic">{ICONS[type] ?? <Shapes size={16} />}</span>
          <span className="prvct">
            <b>{title}</b>
            <span className="small">
              {label}
              {a.root ? " · " + a.root : ""}
            </span>
          </span>
        </button>
        {a.viewUrl && (
          <button className="ix" type="button" title={t("oa.cardBrowser")} aria-label={t("oa.cardBrowser")} onClick={() => openExternal(a.viewUrl!)}>
            <ExternalLink size={14} />
          </button>
        )}
      </div>
    </div>
  );
}
