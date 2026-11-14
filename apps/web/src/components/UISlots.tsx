import {
  Blocks,
  CheckCircle2,
  CircleAlert,
  ExternalLink,
  GitBranch,
  Info,
  Loader2,
  Puzzle,
  Rocket,
  ShieldCheck,
  TriangleAlert,
  Zap,
  type LucideIcon,
} from "lucide-react";
import { useEffect, useState } from "react";
import { API, desktop, type UIContribution, type UIField, type UIPanel } from "../api";
import { t } from "../i18n";
import { openSession, toast, useStore } from "../store";

/** Plugins name icons; only this set is rendered (others fall back). */
const ICONS: Record<string, LucideIcon> = {
  "shield-check": ShieldCheck,
  rocket: Rocket,
  "git-branch": GitBranch,
  zap: Zap,
  blocks: Blocks,
  info: Info,
  "check-circle": CheckCircle2,
  alert: TriangleAlert,
  error: CircleAlert,
};

export function SlotIcon({ name, size = 15 }: { name?: string; size?: number }) {
  const I = (name && ICONS[name]) || Puzzle;
  return <I size={size} />;
}

export function useSlot(slot: UIContribution["slot"]): UIContribution[] {
  const ui = useStore((s) => s.ui);
  return ui.filter((c) => c.slot === slot);
}

/** Resolves {{a.b}} against data. Results are text only. */
export function fillTemplate(tpl: string, data: any): string {
  return tpl.replace(/\{\{\s*([\w.-]+)\s*\}\}/g, (_, path: string) => {
    let v = data;
    for (const k of path.split(".")) v = v == null ? undefined : v[k];
    if (v == null) return "";
    return typeof v === "object" ? JSON.stringify(v) : String(v);
  });
}

const safeHref = (h?: string) => (h && /^https?:\/\//i.test(h) ? h : "");

function openLink(href: string) {
  const d = desktop();
  if (d?.openExternal) d.openExternal(href);
  else window.open(href, "_blank", "noopener,noreferrer");
}

function FieldRows({ fields }: { fields: UIField[] }) {
  return (
    <dl className="uif">
      {fields.map((f, i) => {
        const href = safeHref(f.href);
        return (
          <div key={i}>
            <dt>{f.label}</dt>
            <dd className={f.mono ? "mono" : undefined}>
              {href ? (
                <button type="button" className="linkbtn" onClick={() => openLink(href)}>
                  {f.value || href} <ExternalLink size={11} />
                </button>
              ) : (
                f.value
              )}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}

/** conversation.node: a card for a plugin's session event. */
export function PluginCard({ c, data }: { c: UIContribution; data: any }) {
  const card = c.card!;
  const fields = (card.fields ?? []).map((f) => ({ ...f, value: fillTemplate(f.value, data), href: f.href ? fillTemplate(f.href, data) : undefined })).filter((f) => f.value || f.href);
  const tone = card.tone ?? "info";
  return (
    <div className={"uicard " + tone}>
      <div className="uich">
        <SlotIcon name={card.icon || (tone === "ok" ? "check-circle" : tone === "warn" ? "alert" : tone === "error" ? "error" : "info")} />
        <b>{fillTemplate(card.title, data)}</b>
        <span className="uiown" title={t("ui.from", { owner: c.owner })}>
          {c.owner}
        </span>
      </div>
      {fields.length > 0 && <FieldRows fields={fields} />}
    </div>
  );
}

/** Fetches and renders a tab / settings card panel. */
export function PanelView({ c, session = "" }: { c: UIContribution; session?: string }) {
  const [p, setP] = useState<UIPanel | null>(null);
  const [err, setErr] = useState("");
  const tick = useStore((s) => s.ui);
  useEffect(() => {
    let live = true;
    setErr("");
    API.uiPanel(c.slot, c.key, session)
      .then((r) => live && setP(r))
      .catch((e) => live && setErr(e?.message ?? String(e)));
    return () => {
      live = false;
    };
  }, [c.slot, c.key, session, tick]);
  if (err) return <div className="small dngt">{t("ui.failed", { error: err })}</div>;
  if (!p)
    return (
      <div className="small muted">
        <Loader2 size={13} className="spin" /> {t("common.loading")}
      </div>
    );
  if (!p.sections.length) return <div className="small muted">{p.empty || t("ui.empty")}</div>;
  return (
    <div className="uipanel">
      {p.sections.map((s, i) => (
        <section key={i}>
          {s.title && <div className="uisec">{s.title}</div>}
          {s.text && <p className="small">{s.text}</p>}
          {s.fields && s.fields.length > 0 && <FieldRows fields={s.fields} />}
        </section>
      ))}
    </div>
  );
}

/** Runs a palette.command / composer.action (asks first when it declares confirm). */
export async function runCommand(c: UIContribution, session = "") {
  if (c.confirm && !window.confirm(c.confirm)) return;
  try {
    const r = await API.uiRun(c.slot, c.key, session);
    if (r.toast) toast(r.toast);
    if (r.open) openSession(r.open);
  } catch (e: any) {
    toast(t("ui.failed", { error: e?.message ?? String(e) }));
  }
}
