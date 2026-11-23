// Settings › Open Artifact: the runtime (installed from npm, checked against
// its sha512 digest), the switch, whether the daemon runs, and the library.
// Backed by features/openartifact (GET /api/open-artifact).

import { Skeleton } from "../Skeleton";
import { ExternalLink, Info, RefreshCw, ShieldCheck } from "lucide-react";
import { useEffect, useState } from "react";
import { API, openExternal, type OpenArtifactStatus } from "../../api";
import { t } from "../../i18n";
import { toast } from "../../store";
import { Head, Row } from "../Settings";

const fail = (e: any) => toast(e?.message ?? String(e));

export function OpenArtifactPage() {
  const [st, setSt] = useState<OpenArtifactStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [checked, setChecked] = useState(false);
  const load = () => API.openArtifact().then(setSt, fail);
  useEffect(() => {
    void load();
    // the daemon starts a moment after it is turned on
    const id = setInterval(() => void API.openArtifact().then(setSt, () => {}), 4000);
    return () => clearInterval(id);
  }, []);

  if (!st)
    return (
      <>
        <Head title={t("set.artifacts")} sub={t("sub.artifacts")} />
        <div className="vb">
          <Skeleton />
        </div>
      </>
    );

  const install = async (version?: string) => {
    setBusy(true);
    try {
      const s = await API.installOpenArtifact(version);
      setSt(s);
      toast(t("oa.installed", { version: s.version ?? "" }));
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };
  const check = async () => {
    setBusy(true);
    try {
      const { version } = await API.openArtifactLatest();
      setSt({ ...st, latest: version });
      setChecked(true);
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };
  const installing = busy || !!st.busy;
  const has = !!st.version && st.installed.includes(st.version);
  const newer = has && st.latest && st.latest !== st.version ? st.latest : "";

  return (
    <>
      <Head title={t("set.artifacts")} sub={t("sub.artifacts")} />
      <div className="vb">
        <div className="sbox">
          <Row title={t("oa.node")} desc={st.node.ok ? t("oa.nodeOk", { version: st.node.version ?? "", path: st.node.path ?? "" }) : (st.node.problem ?? "")}>
            {!st.node.ok && <span className="dot err" />}
          </Row>
          <Row title={t("oa.runtime")} desc={has ? t("oa.runtimeAt", { version: st.version! }) : t("oa.runtimeNone")}>
            {installing ? (
              <span className="small muted">{t("oa.installing")}</span>
            ) : !has ? (
              <button className="btn sm pri" type="button" disabled={!st.node.ok} onClick={() => void install()}>
                {t("oa.install")}
              </button>
            ) : newer ? (
              <button className="btn sm pri" type="button" onClick={() => void install(newer)}>
                {t("oa.update", { version: newer })}
              </button>
            ) : checked ? (
              <span className="small muted">{t("oa.upToDate")}</span>
            ) : (
              <button className="btn sm" type="button" onClick={() => void check()}>
                <RefreshCw size={13} />
                {t("oa.check")}
              </button>
            )}
          </Row>
          {has && (
            <Row title={t("oa.switch")} desc={t(st.enabled ? "oa.switchOn" : "oa.switchOff")}>
              <button
                className="tg"
                type="button"
                role="switch"
                aria-checked={st.enabled}
                aria-label={t("oa.switch")}
                onClick={() => void API.setOpenArtifact(!st.enabled).then(setSt, fail)}
              />
            </Row>
          )}
          {has && st.enabled && (
            <Row
              title={t("oa.status")}
              desc={
                st.running
                  ? st.sessions > 0
                    ? t("oa.runningSessions", { daemon: st.daemon ?? "", n: String(st.sessions) })
                    : t("oa.running", { daemon: st.daemon ?? "" })
                  : st.error
                    ? t("oa.down", { error: st.error })
                    : t("oa.starting")
              }
            >
              <span className={"dot " + (st.running ? "ok" : st.error ? "err" : "wait")} />
            </Row>
          )}
          {st.running && st.homeUrl && (
            <Row title={t("oa.library")} desc={t("oa.libraryDesc")}>
              <button className="btn sm" type="button" onClick={() => openExternal(st.homeUrl!)}>
                <ExternalLink size={13} />
                {t("oa.open")}
              </button>
            </Row>
          )}
          <Row title={t("oa.home")} desc={st.home}>{null}</Row>
        </div>

        <div className="vsec">{t("oa.how")}</div>
        <div className="box cusafe">
          <Info size={16} className="muted" />
          <ul>
            <li>{t("oa.how1")}</li>
            <li>{t("oa.how2")}</li>
            <li>{t("oa.how3")}</li>
            <li>{t("oa.how4")}</li>
          </ul>
        </div>
        <p className="small muted cufoot">
          <ShieldCheck size={13} style={{ verticalAlign: "-2px", marginRight: 4 }} />
          {t("oa.safe")}
        </p>
      </div>
    </>
  );
}
