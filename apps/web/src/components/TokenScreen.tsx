import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { API, setToken } from "../api";
import { t } from "../i18n";
import { Logo } from "./Logo";

export function TokenScreen({ onAuthed }: { onAuthed: () => void }) {
  const [v, setV] = useState("");
  const [err, setErr] = useState(false);
  const [busy, setBusy] = useState(false);
  const submit = async () => {
    const tok = v.trim();
    if (!tok) return;
    setBusy(true);
    setToken(tok);
    try {
      await API.agents();
      onAuthed();
    } catch {
      setToken(null);
      setErr(true);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="tokscreen">
      <form
        className="tokcard"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Logo size={48} title="Crewbase" />
        <h1>{t("token.title")}</h1>
        <p>{t("token.desc")}</p>
        <span className="cmdrow">
          <code className="cmd">{t("token.hint")}</code>
          <CopyCmd text={t("token.hint")} />
        </span>
        <input
          className={"fi" + (err ? " bad" : "")}
          autoFocus
          value={v}
          placeholder={t("token.placeholder")}
          spellCheck={false}
          autoComplete="off"
          onChange={(e) => {
            setV(e.target.value);
            setErr(false);
          }}
        />
        {err && <div className="ferr">{t("token.invalid")}</div>}
        <button className="btn pri big" type="submit" disabled={!v.trim() || busy}>
          {t("token.submit")}
        </button>
      </form>
    </div>
  );
}

function CopyCmd({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="ix cmdcopy"
      aria-label={t("msg.copy")}
      title={t("msg.copy")}
      onClick={() =>
        void navigator.clipboard?.writeText(text).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1500);
        })
      }
    >
      {done ? <Check size={14} /> : <Copy size={14} />}
    </button>
  );
}
