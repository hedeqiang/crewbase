import { useEffect, useRef, useState } from "react";
import { UpdatesSection, useAddonUpdates } from "./Updates";
import { Search, X } from "lucide-react";
import { API, DEFAULT_TEAM, type McpServer, type PluginView } from "../../api";
import { t } from "../../i18n";
import { setState, useStore } from "../../store";
import { Head } from "../Settings";
import { PasteInstall } from "./PasteInstall";
import { ApproveDialog, errMsg } from "./ExtParts";

// Brand icons from LobeHub Icons (@lobehub/icons-static-svg, MIT), as in
// ProviderMark, where the package has the mark; the others are the
// companies' own marks from their brand pages or sites (src/assets/brands).
// Anything else gets a letter tile.
import { AccessDialog, GitHubDialog, ScopeDialog } from "./extensions/Dialogs";
import { InstalledView } from "./extensions/Installed";
import { MarketView } from "./extensions/Market";
import { catalogAPI, isDocsServer, type CatItem, type Data, type Shared, type Target, type View } from "./extensions/shared";


// (moved to extensions/shared; still importable from here)
export { verLabel } from "./extensions/shared";

/** 设置 › 插件与技能: a marketplace of built-in services, skills and
 *  third-party plugins, and everything connected or installed. */
let lastData: Data | null = null;
export function Extensions() {
  const want = useStore((s) => s.settingsTab);
  const tick = useStore((s) => s.mcpTick);
  const [view, setView] = useState<View>(want === "mcp" || want === "installed" ? "installed" : "market");
  const [q, setQ] = useState("");
  // what was shown last time: coming back shows it at once, then refreshes
  const [d, setD0] = useState<Data>(() => lastData ?? { catalog: null, servers: null, skills: null, plugins: null, market: null, marketErr: null });
  const setD = (f: (x: Data) => Data) =>
    setD0((x) => {
      const n = f(x);
      lastData = n;
      return n;
    });
  const [approve, setApprove] = useState<PluginView | null>(null);
  const upd = useAddonUpdates();
  const nUpd = upd.d?.items.length ?? 0;
  const [paste, setPaste] = useState<{ text: string; picks?: string[] } | null>(null);
  const [focus, setFocus] = useState<string | null>(null); // server to open in Installed
  const [gh, setGh] = useState<CatItem | null>(null); // GitHub sign-in dialog
  const [scopeOf, setScopeOf] = useState<McpServer | null>(null);
  const [accessOf, setAccessOf] = useState<McpServer | null>(null);
  const teams = useStore((s) => s.teams);
  const team = useStore((s) => s.team);
  // New connections go to the team on screen when there are several teams
  // (only its agents see them); "global" keeps an install for everyone.
  const [toAll, setToAll] = useState(false);
  const target: Target = teams.length > 1 && !toAll ? { context: "team:" + (team || DEFAULT_TEAM) } : {};
  useEffect(() => {
    if (want) {
      setView(want === "mcp" || want === "installed" ? "installed" : "market");
      setState({ settingsTab: null });
    }
  }, [want]);
  // events can trigger reloads in quick succession: only the newest answer
  // counts, and a failed reload keeps what is shown (empty only at first)
  const seq = useRef(0);
  const reload = () => {
    const n = ++seq.current;
    const ok = () => n === seq.current;
    catalogAPI.list().then(
      (catalog) => ok() && setD((x) => ({ ...x, catalog })),
      () => ok() && setD((x) => ({ ...x, catalog: x.catalog ?? { categories: [], items: [] } })),
    );
    API.mcp().then(
      (r) => ok() && setD((x) => ({ ...x, servers: r.servers ?? [] })),
      () => ok() && setD((x) => ({ ...x, servers: x.servers ?? [] })),
    );
    catalogAPI.skills().then(
      (r) => ok() && setD((x) => ({ ...x, skills: r.skills ?? [] })),
      () => ok() && setD((x) => ({ ...x, skills: x.skills ?? [] })),
    );
    API.plugins().then(
      (r) => ok() && setD((x) => ({ ...x, plugins: r.plugins ?? [] })),
      () => ok() && setD((x) => ({ ...x, plugins: x.plugins ?? [] })),
    );
  };
  const reloadMarket = () =>
    API.market().then(
      (r) => setD((x) => ({ ...x, market: r.plugins ?? [], marketErr: null })),
      (e) => setD((x) => ({ ...x, market: [], marketErr: errMsg(e) })),
    );
  useEffect(() => {
    reload();
  }, [tick]);
  useEffect(() => {
    void reloadMarket();
  }, []);

  const shared: Shared = {
    d,
    q: q.trim().toLowerCase(),
    reload,
    reloadMarket,
    onApprove: setApprove,
    onSignIn: setGh,
    onScope: setScopeOf,
    onAccess: setAccessOf,
    target,
    toAll,
    setToAll,
    onManage: (server) => {
      setFocus(server);
      setView("installed");
    },
    focus,
    onPaste: (text, picks) => setPaste({ text, picks }),
    search: null,
  };
  const search = (
    <label className={"search mksearch" + (view === "market" ? " inline" : "")}>
      <Search size={15} />
      <input type="search" value={q} placeholder={t("mk.search")} aria-label={t("mk.search")} onChange={(e) => setQ(e.target.value)} />
      {q && (
        <button type="button" className="ix" aria-label={t("common.close")} onClick={() => setQ("")}>
          <X size={13} />
        </button>
      )}
    </label>
  );
  const installedN = (d.servers ?? []).filter((x) => !isDocsServer(x.name)).length + (d.skills?.length ?? 0) + (d.plugins?.length ?? 0);
  return (
    <>
      <Head
        title={t("set.market")}
        sub={t("mk.sub")}
        right={
          <div className="mkhr">
          <div className="seg" role="group">
            <button type="button" aria-pressed={view === "market"} onClick={() => setView("market")}>
              {t("mk3.tabDiscover")}
            </button>
            <button type="button" aria-pressed={view === "installed"} onClick={() => setView("installed")}>
              {t("mk.tabInstalled")}
              {nUpd > 0 ? <span className="segn upd" title={t("aup.available", { n: nUpd })}>{nUpd}</span> : installedN > 0 && <span className="segn">{installedN}</span>}
            </button>
          </div>
          </div>
        }
      />
      <div className="vb mkwrap">
        {view === "market" ? (
          <>
            {nUpd > 0 && (
              <button className="updbar" type="button" onClick={() => setView("installed")}>
                <span className="updot static" />
                {t("aup.available", { n: nUpd })}
                <span className="sp" />
                {t("aup.see")}
              </button>
            )}
            <MarketView {...shared} search={search} />
          </>
        ) : (
          <>
            <UpdatesSection u={upd} onChanged={reload} onApprove={setApprove} />
            {search}
            <InstalledView {...shared} />
          </>
        )}
      </div>
      {approve && (
        <ApproveDialog
          p={approve}
          onClose={() => setApprove(null)}
          onDone={() => {
            setApprove(null);
            reload();
          }}
        />
      )}
      {paste !== null && (
        <PasteInstall
          initial={paste.text}
          picks={paste.picks}
          onClose={() => setPaste(null)}
          onDone={() => {
            reload();
            setView("installed");
          }}
          onApprove={setApprove}
        />
      )}
      {gh && <GitHubDialog item={gh} target={target} servers={d.servers ?? []} onClose={() => setGh(null)} onChanged={reload} />}
      {accessOf && <AccessDialog server={accessOf} label={d.catalog?.items.find((it) => it.server === accessOf.name)?.name ?? accessOf.name} onClose={() => setAccessOf(null)} />}
      {scopeOf && <ScopeDialog server={scopeOf} label={d.catalog?.items.find((it) => it.server === scopeOf.name)?.name ?? scopeOf.name} onClose={() => setScopeOf(null)} onSaved={reload} />}
    </>
  );
}
