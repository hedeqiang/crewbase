import { useState } from "react";
import { API, setToken } from "../api";
import { t } from "../i18n";

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
        <span className="logo big" aria-hidden />
        <h1>{t("token.title")}</h1>
        <p>{t("token.desc")}</p>
        <code className="cmd">{t("token.hint")}</code>
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
