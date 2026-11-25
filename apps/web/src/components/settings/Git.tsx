// 设置 › Git (features/git): where sub-agents work, how local reviews go,
// protected branches, push credentials per host (vault references only),
// and GitHub pull requests (the first-pass reviewer, private GHE ranges).

import { useEffect, useState } from "react";
import { Plus, X } from "lucide-react";
import { API, type GitSettings } from "../../api";
import { t, type Key } from "../../i18n";
import { setState, toast, useStore } from "../../store";
import { Head, Row } from "../Settings";
import { Skeleton } from "../Skeleton";

const errOf = (e: any) => e?.message ?? String(e);
const ISO: GitSettings["isolation"][] = ["worktree-on-write", "isolated-worktree", "shared"];

export function GitPage() {
  const agents = useStore((x) => x.agents);
  const [saved, setSaved] = useState<GitSettings | null>(null);
  const [s, setS] = useState<GitSettings | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => void API.gitSettings().then((x) => (setSaved(x), setS(x)), (e) => toast(errOf(e))), []);
  if (!s || !saved) {
    return (
      <>
        <Head title={t("set.git")} sub={t("gitset.sub")} />
        <div className="vb"><Skeleton kind="rows" n={5} /></div>
      </>
    );
  }
  const dirty = JSON.stringify(s) !== JSON.stringify(saved);
  const save = async () => {
    setBusy(true);
    try {
      const out = await API.setGitSettings({ ...s, protected: s.protected.filter(Boolean), remotes: s.remotes.filter((r) => r.host.trim()) });
      setSaved(out);
      setS(out);
      toast(t("git.saved"));
    } catch (e) {
      toast(errOf(e));
    } finally {
      setBusy(false);
    }
  };
  const remote = (i: number, patch: Partial<GitSettings["remotes"][number]>) => setS({ ...s, remotes: s.remotes.map((x, j) => (j === i ? { ...x, ...patch } : x)) });
  return (
    <>
      <Head
        title={t("set.git")}
        sub={t("gitset.sub")}
        right={
          <>
            {dirty && <button type="button" className="btn sm ghost" onClick={() => setS(saved)}>{t("common.cancel")}</button>}
            <button type="button" className="btn sm pri" disabled={!dirty || busy} onClick={() => void save()}>{t("common.save")}</button>
          </>
        }
      />
      <div className="vb gitset">
        <div className="vsec">{t("git.set.remotes")}</div>
        <div className="sbox">
          <div className="gsremotes">
            <p className="small gsnote">{t("git.set.remotesHint")}</p>
            {s.remotes.length > 0 && (
              <div className="gsrh small">
                <span>{t("gitset.host")}</span>
                <span>{t("gitset.cred")}</span>
                <span>{t("git.set.apiPh")}</span>
                <span />
              </div>
            )}
            {s.remotes.map((r, i) => (
              <div key={i} className="gsr">
                <input className="fi sm" value={r.host} placeholder="github.com" aria-label={t("gitset.host")} onChange={(e) => remote(i, { host: e.target.value.trim() })} />
                <input className="fi sm mono" value={r.credential} placeholder="vault:github/token" aria-label={t("gitset.cred")} onChange={(e) => remote(i, { credential: e.target.value.trim() })} />
                <input className="fi sm mono" value={r.api ?? ""} placeholder="https://api.github.com" title={t("git.set.apiHint")} aria-label={t("git.set.apiPh")} onChange={(e) => remote(i, { api: e.target.value.trim() || undefined })} />
                <button type="button" className="ix" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => setS({ ...s, remotes: s.remotes.filter((_, j) => j !== i) })}>
                  <X size={13} />
                </button>
              </div>
            ))}
            <div className="gsadd">
              <button type="button" className="btn sm" onClick={() => setS({ ...s, remotes: [...s.remotes, { host: "github.com", credential: "vault:" }] })}>
                <Plus size={12} />
                {t("git.set.addRemote")}
              </button>
              <button type="button" className="linkbtn small" onClick={() => setState({ settings: "vault" })}>{t("gitset.toVault")}</button>
            </div>
          </div>
        </div>

        <div className="vsec">{t("git.set.github")}</div>
        <div className="sbox">
          <Row title={t("git.set.prReviewer")} desc={t("git.set.prReviewerHint")}>
            <span className="ssel">
              <select value={s.prReviewer ?? ""} onChange={(e) => setS({ ...s, prReviewer: e.target.value || undefined })}>
                <option value="">{t("git.set.prReviewerNone")}</option>
                {Object.values(agents)
                  .filter((a) => a.kind !== "chat")
                  .map((a) => (
                    <option key={a.id} value={a.id}>{a.name}</option>
                  ))}
              </select>
            </span>
          </Row>
          <Row title={t("git.set.allowPrivate")} desc={t("git.set.allowPrivateHint")}>
            <input className="fi sm mono gsw" value={(s.allowPrivate ?? []).join(", ")} placeholder="10.0.0.0/8" onChange={(e) => setS({ ...s, allowPrivate: e.target.value.split(",").map((x) => x.trim()).filter(Boolean) })} />
          </Row>
          <p className="small gsnote gspad">{t("git.set.forkHint")}</p>
        </div>

        <div className="vsec">{t("git.set.protected")}</div>
        <div className="sbox">
          <Row title={t("git.set.protected")} desc={t("git.set.protectedHint")}>
            <input className="fi sm mono gsw" value={s.protected.join(", ")} placeholder="main, release/*" onChange={(e) => setS({ ...s, protected: e.target.value.split(",").map((x) => x.trim()) })} />
          </Row>
        </div>

        <div className="vsec">{t("git.set.isolation")}</div>
        <div className="sbox gsopts">
          {ISO.map((k) => (
            <label key={k} className={"gsopt" + (s.isolation === k ? " on" : "")}>
              <input type="radio" name="iso" checked={s.isolation === k} onChange={() => setS({ ...s, isolation: k })} />
              <span className="g">
                <b>{t(`git.iso.${k}` as Key)}</b>
                <span className="small">{t(`git.iso.${k}.hint` as Key)}</span>
              </span>
            </label>
          ))}
        </div>

        <div className="vsec">{t("git.set.review")}</div>
        <div className="sbox">
          <Row title={t("git.set.rounds")}>
            <input className="fi sm gsnum" type="number" min={0} max={10} value={s.maxAutoRounds} onChange={(e) => setS({ ...s, maxAutoRounds: Number(e.target.value) })} />
          </Row>
          <Row title={t("git.set.approvals")}>
            <input className="fi sm gsnum" type="number" min={1} max={5} value={s.approvals} onChange={(e) => setS({ ...s, approvals: Number(e.target.value) })} />
          </Row>
          <Row title={t("git.set.autoReview")}>
            <button type="button" className="tg" role="switch" aria-checked={s.autoReview} aria-label={t("git.set.autoReview")} onClick={() => setS({ ...s, autoReview: !s.autoReview })} />
          </Row>
        </div>
      </div>
    </>
  );
}
