import { keyLabel } from "../shortcuts";
import { Check, KeyRound, Loader2, MessageSquare, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { modelKind } from "../format";
import { t, type Key } from "../i18n";
import { newChat, openConnect, openNewAgent, openSettings, useStore, teamMode } from "../store";
import { AvatarRaw } from "./Avatar";
import { Logo } from "./Logo";
import { ShuffleButton, TEMPLATES, TEMPLATE_CATS, templatePage, type Draft, type TemplateCat } from "./NewAgent";
import { ProviderMark } from "./ProviderMark";

/** Shown in the main area when there is no conversation open. With no
 *  agents yet it is the first-run welcome: what Crewbase found on this
 *  machine and three ways to start; otherwise a quiet "pick one" note. */
export function Welcome({ onTemplate }: { onTemplate: (d: Partial<Draft>) => void }) {
  const agents = useStore((s) => s.agents);
  const models = useStore((s) => s.models);
  const name = useStore((s) => s.profile.displayName);
  // blank chats are not a crew yet: the welcome (with templates) stays
  // until the first real agent exists
  const first = !Object.values(agents).some((a) => a.kind !== "chat");
  const [page, setPage] = useState(0);
  const [cat, setCat] = useState<TemplateCat | "">("");

  const sources = useMemo(() => {
    const ps = models?.providers ?? [];
    return ps
      .map((p) => ({ p, chat: (p.models ?? []).filter((m) => modelKind(m) === "chat") }))
      .filter(({ p, chat }) => p.id !== "mock" && chat.length > 0)
      .map(({ p, chat }) => ({ id: p.id, kind: p.kind, n: chat.length, model: chat[0]?.id }));
  }, [models]);

  const ready = sources.length > 0 || (models?.providers ?? []).some((p) => (p.models?.length ?? 0) > 0);

  // keep the steps up until a model is connected, even after a first chat
  if (!first && (ready || models === null)) {
    return (
      <div className="welcome quiet">
        <Logo size={40} />
        <b>{t("wel.pick")}</b>
        <span className="small">{t("wel.pickDesc", { key: keyLabel("palette") })}</span>
      </div>
    );
  }

  const start = (key: (typeof TEMPLATES)[number]["key"]) => {
    const x = TEMPLATES.find((tp) => tp.key === key)!;
    onTemplate({
      name: t(`tpl.${key}.name` as Key),
      desc: t(`tpl.${key}.${x.instr ? "instr" : "desc"}` as Key),
      role: t(`tpl.${key}.role` as Key),
      color: x.color,
      shape: x.shape,
      face: x.face,
      mode: teamMode(x.mode),
    });
    openNewAgent();
  };

  return (
    <div className="welcome">
      <Logo size={48} />
      <h1>{name ? t("wel.hiName", { name }) : t("wel.hi")}</h1>
      <p className="lede">{t("wel.lede")}</p>

      <div className={"wstep" + (ready ? " done" : "")}>
        <div className="wstep-h">
          <span className="wnum">{ready ? <Check size={13} /> : 1}</span>
          <b>{ready ? t("wel.step1Done") : t("wel.step1")}</b>
        </div>
        {models === null ? (
          <p className="small muted wstep-b">
            <Loader2 size={13} className="spin" /> {t("wel.looking")}
          </p>
        ) : sources.length > 0 ? (
          <div className="wsrc-l wstep-b">
            {sources.slice(0, 6).map((s) => (
              <span key={s.id} className="wchip" title={s.id}>
                <ProviderMark provider={s.id} model={s.model} size={14} />
                <span>{providerTitle(s.id)}</span>
                <span className="muted">{s.kind === "subscription" ? t("wel.sub") : s.kind === "local" ? t("wel.local") : t("wel.models", { n: s.n })}</span>
              </span>
            ))}
            {sources.length > 6 && <span className="wchip muted">+{sources.length - 6}</span>}
            <button className="btn sm ghost" type="button" onClick={openConnect}>
              {t("wel.addMore")}
            </button>
          </div>
        ) : (
          <div className="wstep-b">
            <p className="small muted">{t("wel.step1Desc")}</p>
            <div className="wconn">
              <button className="btn pri" type="button" onClick={openConnect}>
                <KeyRound size={14} />
                {t("wel.connectKey")}
              </button>
              <button className="btn" type="button" onClick={() => openSettings("models")}>
                {t("wel.seeModels")}
              </button>
            </div>
            <p className="small muted wfine">{t("wel.autoFound")}</p>
          </div>
        )}
      </div>

      <div className={"wstep" + (ready ? "" : " later")}>
        <div className="wstep-h">
          <span className="wnum">2</span>
          <b>{t("wel.step2")}</b>
        </div>
      </div>
      <div className="wtplh">
        <span>{t("na.templates")}</span>
        <ShuffleButton page={page} cat={cat} onNext={() => setPage((p) => p + 1)} />
      </div>
      <div className="wcats" role="tablist" aria-label={t("na.templates")}>
        {(["", ...TEMPLATE_CATS] as (TemplateCat | "")[]).map((c) => (
          <button
            key={c || "all"}
            type="button"
            role="tab"
            aria-selected={cat === c}
            className={"wcat" + (cat === c ? " on" : "")}
            onClick={() => {
              setCat(c);
              setPage(0);
            }}
          >
            {c ? t(`tplcat.${c}` as Key) : t("tplcat.all")}
          </button>
        ))}
      </div>
      <div className="wtpl">
        {templatePage(page, cat).map((x) => (
          <button key={x.key} type="button" className="wcard" onClick={() => start(x.key)}>
            <AvatarRaw color={x.color} shape={x.shape} face={x.face} size={36} name={t(`tpl.${x.key}.name` as Key)} />
            <b>{t(`tpl.${x.key}.name` as Key)}</b>
            <span>{t(`tpl.${x.key}.desc` as Key)}</span>
          </button>
        ))}
      </div>

      <div className="wacts">
        <button className="btn pri wnew" type="button" onClick={() => openNewAgent()}>
          <Plus size={15} />
          {t("wel.blank")}
        </button>
        <button className="btn wnew" type="button" onClick={() => void newChat()}>
          <MessageSquare size={15} />
          {t("wel.chat")}
        </button>
      </div>
      <p className="small wfoot">{t("wel.footer")}</p>
    </div>
  );
}

function providerTitle(id: string): string {
  const m: Record<string, string> = {
    "acp-claude-code": "Claude Code",
    "acp-codex": "Codex",
    "acp-antigravity": "Antigravity",
    "acp-cursor": "Cursor",
    "acp-opencode": "OpenCode",
    "acp-grok": "Grok CLI",
    magpie: "magpie",
    ollama: "Ollama",
    lmstudio: "LM Studio",
    vllm: "vLLM",
    deepseek: "DeepSeek",
    anthropic: "Anthropic",
    openai: "OpenAI",
    gemini: "Gemini",
  };
  return m[id] ?? id;
}
