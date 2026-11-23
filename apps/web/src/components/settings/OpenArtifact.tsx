// Open Artifact (features/openartifact), handled like Open Agent Docs: a row
// under 插件与技能 › 应用 opens a dialog that installs and switches it, and
// once it is installed Settings › Open Artifact shows the same controls.
// The runtime comes from npm, checked against its sha512 digest.

import { Skeleton } from "../Skeleton";
import { ExternalLink, Info, RefreshCw, Shapes, ShieldCheck, X } from "lucide-react";
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { API, openExternal, type OpenArtifactStatus } from "../../api";
import { t } from "../../i18n";
import { refreshOpenArtifact, toast } from "../../store";
import { Head, Row } from "../Settings";

const fail = (e: any) => toast(e?.message ?? String(e));
const REPO = "https://github.com/JimLiu/open-artifact";

/** The status, polled while shown (the daemon starts a moment after it is turned on). */
export function useOpenArtifact() {
  const [st, setSt] = useState<OpenArtifactStatus | null>(null);
  useEffect(() => {
    void API.openArtifact().then(setSt, () => setSt(null));
    const id = setInterval(() => void API.openArtifact().then(setSt, () => {}), 4000);
    return () => clearInterval(id);
  }, []);
  return { st, setSt };
}

export function OpenArtifactIcon({ size = 36 }: { size?: number }) {
  return (
    <span className="adicon oaicon" style={{ width: size, height: size }}>
      <Shapes size={Math.round(size * 0.5)} />
    </span>
  );
}

/** Whether it is installed (the 插件与技能 row's state). */
export const oaInstalled = (st: OpenArtifactStatus | null) => !!st?.version && st.installed.includes(st.version);

/** The controls: Node, the runtime, the switch, the daemon, the library. */
function Body({ st, setSt }: { st: OpenArtifactStatus; setSt: (s: OpenArtifactStatus) => void }) {
  const [busy, setBusy] = useState(false);
  const [checked, setChecked] = useState(false);
  const changed = (s: OpenArtifactStatus) => {
    setSt(s);
    void refreshOpenArtifact();
  };
  const install = async (version?: string) => {
    setBusy(true);
    try {
      const s = await API.installOpenArtifact(version);
      changed(s);
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
  const has = oaInstalled(st);
  const newer = has && st.latest && st.latest !== st.version ? st.latest : "";
  return (
    <>
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
            <button className="tg" type="button" role="switch" aria-checked={st.enabled} aria-label={t("oa.switch")} onClick={() => void API.setOpenArtifact(!st.enabled).then(changed, fail)} />
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
        <Row title={t("oa.home")} desc={st.home}>
          {null}
        </Row>
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
    </>
  );
}

/** 插件与技能 › 应用 › Open Artifact. */
export function OpenArtifactDialog({ onClose }: { onClose: () => void }) {
  const { st, setSt } = useOpenArtifact();
  useEffect(() => {
    const k = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", k);
    return () => window.removeEventListener("keydown", k);
  }, [onClose]);
  return createPortal(
    <>
      <div className="scrim" onClick={onClose} />
      <div className="modal mkdlg addlg oadlg" role="dialog" aria-modal="true" aria-labelledby="oaTitle">
        <div className="mhd">
          <OpenArtifactIcon size={32} />
          <h3 id="oaTitle">Open Artifact</h3>
          <button className="ix" type="button" aria-label={t("common.close")} onClick={onClose}>
            <X size={14} />
          </button>
        </div>
        <div className="mbd">
          <p className="small addesc">
            {t("oa.desc")}{" "}
            <button className="linkbtn" type="button" onClick={() => openExternal(REPO)}>
              GitHub
            </button>
          </p>
          {st ? <Body st={st} setSt={setSt} /> : <Skeleton />}
        </div>
      </div>
    </>,
    document.body,
  );
}

/** The library can be framed only from the computer the daemon runs on
 *  (it listens on 127.0.0.1). */
function embeddable() {
  return typeof location !== "undefined" && ["127.0.0.1", "localhost", "[::1]", "::1"].includes(location.hostname);
}

/** 设置 › Open Artifact: the library (every artifact), like Open Agent Docs'
 *  workspace; 管理 opens the setup dialog. Listed once it is turned on. */
export function OpenArtifactPage() {
  const { st } = useOpenArtifact();
  const [manage, setManage] = useState(false);
  const url = st?.running ? st.homeUrl : undefined;
  const sub = st ? [st.daemon && `Open Artifact ${st.daemon}`, st.running ? t("oa.libraryDesc") : st.error ? t("oa.down", { error: st.error }) : t("oa.starting")].filter(Boolean).join(" · ") : "";
  return (
    <>
      <Head
        title="Open Artifact"
        sub={sub}
        right={
          <span className="docshr">
            {url && (
              <button className="btn sm" type="button" onClick={() => openExternal(url)}>
                <ExternalLink size={12} />
                {t("oa.cardBrowser")}
              </button>
            )}
            <button className="btn sm" type="button" onClick={() => setManage(true)}>
              {t("mk2.manage")}
            </button>
          </span>
        }
      />
      <div className="docswrap">
        {!st ? (
          <Skeleton />
        ) : !url ? (
          <div className="docsframe loading small muted">{st.error ? t("oa.down", { error: st.error }) : t("oa.starting")}</div>
        ) : !embeddable() ? (
          <div className="docsout">
            <p className="small muted">{t("oa.notHere")}</p>
          </div>
        ) : (
          <iframe className="docsframe" src={url} title="Open Artifact" allow="clipboard-read; clipboard-write; fullscreen" />
        )}
      </div>
      {manage && <OpenArtifactDialog onClose={() => setManage(false)} />}
    </>
  );
}
