import { ArrowRight, Plus } from "lucide-react";
import { useMemo, useState } from "react";
import { t, type Key } from "../i18n";
import { openNewAgent, openSettings, useStore } from "../store";
import { AvatarRaw } from "./Avatar";
import { Logo } from "./Logo";
import { ShuffleButton, TEMPLATES, templatePage, type Draft } from "./NewAgent";
import { ProviderMark } from "./ProviderMark";

/** Shown in the main area when there is no conversation open. With no
 *  agents yet it is the first-run welcome: what Crewbase found on this
 *  machine and three ways to start; otherwise a quiet "pick one" note. */
export function Welcome({ onTemplate }: { onTemplate: (d: Partial<Draft>) => void }) {
  const agents = useStore((s) => s.agents);
  const models = useStore((s) => s.models);
  const name = useStore((s) => s.profile.displayName);
  const first = Object.keys(agents).length === 0;
  const [page, setPage] = useState(0);

  const sources = useMemo(() => {
    const ps = models?.providers ?? [];
    return ps
      .filter((p) => p.id !== "mock" && (p.models?.length ?? 0) > 0)
      .map((p) => ({ id: p.id, kind: p.kind, n: p.models.length, model: p.models[0]?.id }));
  }, [models]);

  if (!first) {
    return (
      <div className="welcome quiet">
        <Logo size={40} />
        <b>{t("wel.pick")}</b>
        <span className="small">{t("wel.pickDesc")}</span>
      </div>
    );
  }

  const start = (key: (typeof TEMPLATES)[number]["key"]) => {
    const x = TEMPLATES.find((tp) => tp.key === key)!;
    onTemplate({
      name: t(`tpl.${key}.name` as Key),
      desc: t(`tpl.${key}.desc` as Key),
      role: t(`tpl.${key}.role` as Key),
      color: x.color,
      shape: x.shape,
      mode: x.mode,
    });
    openNewAgent();
  };

  return (
    <div className="welcome">
      <Logo size={48} />
      <h1>{name ? t("wel.hiName", { name }) : t("wel.hi")}</h1>
      <p className="lede">{t("wel.lede")}</p>

      <div className="wsrc">
        <div className="wsrc-h">{t("wel.found")}</div>
        {sources.length > 0 ? (
          <div className="wsrc-l">
            {sources.slice(0, 6).map((s) => (
              <span key={s.id} className="wchip" title={s.id}>
                <ProviderMark provider={s.id} model={s.model} size={14} />
                <span>{providerTitle(s.id)}</span>
                <span className="muted">{s.kind === "subscription" ? t("wel.sub") : s.kind === "local" ? t("wel.local") : t("wel.models", { n: s.n })}</span>
              </span>
            ))}
            {sources.length > 6 && <span className="wchip muted">+{sources.length - 6}</span>}
          </div>
        ) : (
          <button className="linkbtn" type="button" onClick={() => openSettings("models")}>
            {t("wel.noModels")} <ArrowRight size={13} />
          </button>
        )}
      </div>

      <div className="wtplh">
        <span>{t("na.templates")}</span>
        <ShuffleButton page={page} onNext={() => setPage((p) => p + 1)} />
      </div>
      <div className="wtpl">
        {templatePage(page).map((x) => (
          <button key={x.key} type="button" className="wcard" onClick={() => start(x.key)}>
            <AvatarRaw color={x.color} shape={x.shape} size={36} name={t(`tpl.${x.key}.name` as Key)} />
            <b>{t(`tpl.${x.key}.name` as Key)}</b>
            <span>{t(`tpl.${x.key}.desc` as Key)}</span>
          </button>
        ))}
      </div>

      <button className="btn pri wnew" type="button" onClick={openNewAgent}>
        <Plus size={15} />
        {t("wel.blank")}
      </button>
      <p className="small wfoot">{t("wel.footer")}</p>
    </div>
  );
}

function providerTitle(id: string): string {
  const m: Record<string, string> = {
    "acp-claude-code": "Claude Code",
    "acp-codex": "Codex",
    "acp-gemini-cli": "Gemini CLI",
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
