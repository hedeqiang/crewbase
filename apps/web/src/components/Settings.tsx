import { useState } from "react";
import { ChevronLeft, Search } from "lucide-react";
import { NAV, SECTION_ICONS } from "./settings/nav";
import { Head, Row } from "./settings/Head";
import { catLabel } from "./settings/labels";
import { t, type Key } from "../i18n";
import { setState, useStore, type SettingsSection } from "../store";
import { Webhooks } from "./settings/Webhooks";
import { ProxyPage } from "./settings/Proxy";
import { GitPage } from "./settings/Git";
import { Extensions } from "./settings/Extensions";
import { Profile } from "./settings/Profile";
import { Vault } from "./settings/Vault";
import { Hosts } from "./settings/Hosts";
import { Team } from "./settings/Team";
import { Teams } from "./settings/Teams";
import { Computer } from "./settings/Computer";
import { Notify } from "./settings/Notify";
import { Im } from "./settings/Im";
import { DocsPage } from "./settings/AgentDocs";
import { OpenArtifactPage } from "./settings/OpenArtifact";
import { leaveTo } from "../router";
export { Head, Row, catLabel };
// moved to settings/GeneralPage (still importable from here)
export { useSettingsData } from "./settings/GeneralPage";
import { Keys } from "./settings/KeysPage";
import { General } from "./settings/GeneralPage";
import { Models } from "./settings/ModelsPage";
import { Approvals } from "./settings/ApprovalsPage";



/** What a phone may open in settings. */
const MOBILE_SECTIONS = new Set<string>(["general", "keys", "approvals"]);

export function Settings() {
  const sec = useStore((s) => s.settings);
  const list = useStore((s) => s.settingsList);
  const n = useStore((s) => s.approvals.length);
  const nUpd = useStore((s) => s.addonUpdates);
  const docsOn = useStore((s) => !!s.agentDocs);
  const oaOn = useStore((s) => s.openArtifact);
  // apps set up under 插件与技能 get a section once they are there
  const shown = (id: string) => (id !== "docs" || docsOn) && (id !== "artifacts" || oaOn);
  const manage = useStore((s) => s.tier === "desktop");
  const [q, setQ] = useState("");
  const ql = q.trim().toLowerCase();
  return (
    <div className={"setwrap" + (list ? " slist" : " spage")}>
      <nav className="snav" aria-label={t("tb.settings")}>
        <button className="back" type="button" onClick={() => leaveTo("chat")}>
          <ChevronLeft size={16} />
          {t("set.back")}
        </button>
        <label className="search sset">
          <Search size={14} />
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t("set.search")} />
        </label>
        {NAV.map((g) => {
          const items = g.items.filter((i) => shown(i.id) && (manage || MOBILE_SECTIONS.has(i.id)) && (!ql || matchesSection(i.id, ql)));
          if (!items.length) return null;
          return (
            <div key={g.group} className="sgrp">
              <div className="sh">{t(g.group)}</div>
              {items.map((i) => (
                <button key={i.id} type="button" aria-current={sec === i.id && !list} onClick={() => setState({ settings: i.id, settingsList: false })}>
                  {i.icon}
                  {t(`set.${i.id}` as Key)}
                  {i.id === "approvals" && n > 0 && <span className="sn">{n}</span>}
                  {i.id === "market" && nUpd > 0 && <span className="sn upd" title={t("aup.dot", { n: nUpd })}>{nUpd}</span>}
                </button>
              ))}
            </div>
          );
        })}
        {ql && !NAV.some((g) => g.items.some((i) => (manage || MOBILE_SECTIONS.has(i.id)) && matchesSection(i.id, ql))) && (
          <p className="small muted snomatch">{t("set.searchNone")}</p>
        )}
      </nav>
      {/* a phone: from a section back to the list of sections */}
      <button className="sback" type="button" onClick={() => setState({ settingsList: true })}>
        <ChevronLeft size={16} />
        {t("tb.settings")}
      </button>
      <div className="vw" data-sec={sec} data-width={FULL_SECTIONS.has(sec) ? "full" : WIDE_SECTIONS.has(sec) ? "wide" : undefined}>
        {!manage && <p className="tiernote">{t("tier.mobileNote")}</p>}
        {(sec === "general" || (!manage && !MOBILE_SECTIONS.has(sec))) && <General />}
        {manage && sec === "models" && <Models />}
        {sec === "approvals" && <Approvals />}
        {manage && sec === "webhooks" && <Webhooks />}
        {manage && sec === "proxy" && <ProxyPage />}
        {manage && sec === "git" && <GitPage />}
        {manage && sec === "market" && <Extensions />}
        {manage && sec === "docs" && <DocsPage />}
        {manage && sec === "artifacts" && <OpenArtifactPage />}
        {manage && sec === "profile" && <Profile />}
        {manage && sec === "vault" && <Vault />}
        {sec === "keys" && <Keys />}
        {manage && sec === "notify" && <Notify />}
        {manage && sec === "hosts" && <Hosts />}
        {manage && sec === "team" && <Team />}
        {manage && sec === "teams" && <Teams />}
        {manage && sec === "im" && <Im />}
        {manage && sec === "computer" && <Computer />}
        {manage && !["general", "models", "approvals", "webhooks", "proxy", "git", "market", "docs", "artifacts", "profile", "vault", "keys", "notify", "hosts", "team", "teams", "im", "computer"].includes(sec) && <Soon sec={sec} />}
      </div>
    </div>
  );
}

/** pages laid out wider (tables, grids, a file browser); the others read
 *  in one column of about 760px */
const WIDE_SECTIONS = new Set<string>(["market", "profile"]);
/** pages with their own panes (a list beside details, a file browser) */
const FULL_SECTIONS = new Set<string>(["models", "docs", "artifacts"]);


/** A settings section matches its title or what is on the page (set.kw.*,
 *  comma-separated, in the current language). */
function matchesSection(id: string, ql: string): boolean {
  const kw = t(`set.kw.${id}` as Key);
  return [t(`set.${id}` as Key), ...(kw.startsWith("set.kw.") ? [] : kw.split(","))].some((x) => x.trim().toLowerCase().includes(ql));
}

function Soon({ sec }: { sec: SettingsSection }) {
  return (
    <>
      <Head title={t(`set.${sec}` as Key)} />
      <div className="vb">
        <div className="soon">
          <div className="chico">{SECTION_ICONS[sec]}</div>
          <b>{t("common.soon")}</b>
          <span className="small">{t("common.soonDesc")}</span>
        </div>
      </div>
    </>
  );
}
