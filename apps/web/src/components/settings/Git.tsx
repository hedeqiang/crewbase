// 设置 › Git (features/git): push credentials per host (optional, vault
// references only), GitHub (through the local gh), protected branches,
// where sub-agents work, and how local reviews go. Every box is made of
// rows (title, description, control), like the other settings pages.

import { useEffect, useRef, useState } from "react";
import { Plus, X } from "lucide-react";
import { API, type GitSettings } from "../../api";
import { t, type Key } from "../../i18n";
import { setState, toast, useStore } from "../../store";
import { Head, Row } from "../Settings";
import { Skeleton } from "../Skeleton";

const errOf = (e: any) => e?.message ?? String(e);
const ISO: GitSettings["isolation"][] = ["worktree-on-write", "isolated-worktree", "shared"];

// what was being edited when you went to another section: back as you left it
let kept: GitSettings | null = null;
const splitBranches = (v: string) => v.split(",").map((x) => x.trim()).filter(Boolean);

export function GitPage() {
  const agents = useStore((x) => x.agents);
  const [saved, setSaved] = useState<GitSettings | null>(null);
  const [s, setS] = useState<GitSettings | null>(null);
  const [busy, setBusy] = useState(false);
  // 需要批准的分支 as typed (split into branches only when saved)
  const [prot, setProt] = useState<string | null>(null);
  useEffect(
    () =>
      void API.gitSettings().then(
        (x) => {
          setSaved(x);
          const k = kept;
          kept = null;
          setS(k ?? x);
          setProt((k ?? x).protected.join(", "));
        },
        (e) => toast(errOf(e)),
      ),
    [],
  );
  const cur = s && prot !== null ? { ...s, protected: splitBranches(prot) } : s;
  const live = useRef({ cur, dirty: false });
  useEffect(() => () => void (live.current.dirty && (kept = live.current.cur)), []);
  if (!s || !saved) {
    return (
      <>
        <Head title={t("set.git")} sub={t("gitset.sub")} />
        <div className="vb"><Skeleton kind="rows" n={5} /></div>
      </>
    );
  }
  const dirty = JSON.stringify(cur) !== JSON.stringify(saved);
  live.current = { cur, dirty };
  const save = async () => {
    if (!dirty || busy) return;
    setBusy(true);
    try {
      const out = await API.setGitSettings({ ...cur!, remotes: s.remotes.filter((r) => r.host.trim()) });
      setSaved(out);
      setS(out);
      setProt(out.protected.join(", "));
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
      />
      {/* Enter in a field saves, as in a form */}
      <form
        className="vb gitset"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <button type="submit" hidden aria-hidden tabIndex={-1} />
        <div className="vsec">{t("git.set.remotes")}</div>
        <div className="sbox">
          <Row title={t("gitset.remotesTitle")} desc={t("git.set.remotesHint")}>
            <button type="button" className="btn sm" onClick={() => setS({ ...s, remotes: [...s.remotes, { host: "github.com", credential: "vault:" }] })}>
              <Plus size={12} />
              {t("git.set.addRemote")}
            </button>
          </Row>
          {s.remotes.map((r, i) => (
            <div key={i} className="srow gsr">
              <input className="fi sm" value={r.host} placeholder="github.com" aria-label={t("gitset.host")} onChange={(e) => remote(i, { host: e.target.value.trim() })} />
              <input className="fi sm mono" value={r.credential} placeholder="vault:github/token" aria-label={t("gitset.cred")} onChange={(e) => remote(i, { credential: e.target.value.trim() })} />
              <button type="button" className="ix" aria-label={t("common.delete")} title={t("common.delete")} onClick={() => setS({ ...s, remotes: s.remotes.filter((_, j) => j !== i) })}>
                <X size={13} />
              </button>
            </div>
          ))}
          {s.remotes.length > 0 && (
            <div className="srow gsfoot">
              <button type="button" className="linkbtn small" onClick={() => setState({ settings: "vault" })}>{t("gitset.toVault")}</button>
            </div>
          )}
        </div>

        <div className="vsec">{t("git.set.github")}</div>
        <div className="sbox">
          <Row title={t("gitset.ghTitle")} desc={t("git.set.viaGH")}>
            <code className="gscmd">gh auth login</code>
          </Row>
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
          <Row title={t("gitset.forkTitle")} desc={t("git.set.forkHint")}>
            <span className="gstag">{t("gitset.readOnly")}</span>
          </Row>
        </div>

        <div className="vsec">{t("git.set.protected")}</div>
        <div className="sbox">
          <Row title={t("gitset.protectedTitle")} desc={t("git.set.protectedHint")}>
            <input className="fi sm mono gsw" value={prot ?? ""} placeholder="main, release/*" aria-label={t("gitset.protectedTitle")} onChange={(e) => setProt(e.target.value)} />
          </Row>
        </div>

        <div className="vsec">{t("git.set.isolation")}</div>
        <div className="sbox" role="radiogroup" aria-label={t("git.set.isolation")}>
          {ISO.map((k) => (
            <label key={k} className={"srow gsopt" + (s.isolation === k ? " on" : "")}>
              <span className="g">
                <b>{t(`git.iso.${k}` as Key)}</b>
                <span>{t(`git.iso.${k}.hint` as Key)}</span>
              </span>
              <span className="c">
                <input type="radio" name="iso" checked={s.isolation === k} onChange={() => setS({ ...s, isolation: k })} />
              </span>
            </label>
          ))}
        </div>

        <div className="vsec">{t("git.set.review")}</div>
        <div className="sbox">
          <Row title={t("git.set.rounds")} desc={t("gitset.roundsHint")}>
            <input className="fi sm gsnum" type="number" min={0} max={10} value={s.maxAutoRounds} onChange={(e) => setS({ ...s, maxAutoRounds: Number(e.target.value) })} />
          </Row>
          <Row title={t("git.set.approvals")} desc={t("gitset.approvalsHint")}>
            <input className="fi sm gsnum" type="number" min={1} max={5} value={s.approvals} onChange={(e) => setS({ ...s, approvals: Number(e.target.value) })} />
          </Row>
          <Row title={t("git.set.autoReview")} desc={t("gitset.autoReviewHint")}>
            <button type="button" className="tg" role="switch" aria-checked={s.autoReview} aria-label={t("git.set.autoReview")} onClick={() => setS({ ...s, autoReview: !s.autoReview })} />
          </Row>
        </div>
        <div className={"savebar" + (dirty ? " show" : "")} aria-hidden={!dirty}>
          <span className="g">{t("wf.unsaved")}</span>
          <button className="btn ghost sm" type="button" tabIndex={dirty ? 0 : -1} onClick={() => (setS(saved), setProt(saved.protected.join(", ")))}>
            {t("common.cancel")}
          </button>
          <button className="btn pri sm" type="submit" disabled={busy} tabIndex={dirty ? 0 : -1}>
            {t("common.save")}
          </button>
        </div>
      </form>
    </>
  );
}
