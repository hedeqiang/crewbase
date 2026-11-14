// Voice: dictation into the composer (Web Speech API, on-device or the
// browser's own service) and reading replies aloud (speechSynthesis).
// Nothing is sent to Crewbase or a model provider until the person sends
// the dictated text.

import { getLang } from "./i18n";

type Rec = {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  abort(): void;
  onresult: ((e: any) => void) | null;
  onerror: ((e: any) => void) | null;
  onend: (() => void) | null;
};

function ctor(): (new () => Rec) | null {
  const w = window as any;
  return w.SpeechRecognition || w.webkitSpeechRecognition || null;
}

export function dictationSupported(): boolean {
  return !!ctor();
}

export function speechLang(): string {
  return getLang() === "en" ? "en-US" : "zh-CN";
}

/** Starts dictation. onText gets the final text so far plus the interim
 *  tail; onEnd is called once when it stops (error = a reason to show). */
export function dictate(onText: (final: string, interim: string) => void, onEnd: (error?: string) => void): () => void {
  const C = ctor();
  if (!C) {
    onEnd("unsupported");
    return () => {};
  }
  const r = new C();
  r.lang = speechLang();
  r.continuous = true;
  r.interimResults = true;
  let final = "";
  let ended = false;
  r.onresult = (e: any) => {
    let interim = "";
    for (let i = e.resultIndex; i < e.results.length; i++) {
      const res = e.results[i];
      if (res.isFinal) final += res[0].transcript;
      else interim += res[0].transcript;
    }
    onText(final, interim);
  };
  r.onerror = (e: any) => {
    if (!ended) {
      ended = true;
      onEnd(e?.error === "not-allowed" || e?.error === "service-not-allowed" ? "denied" : e?.error === "no-speech" ? "silence" : e?.error || "error");
    }
  };
  r.onend = () => {
    if (!ended) {
      ended = true;
      onEnd();
    }
  };
  try {
    r.start();
  } catch {
    ended = true;
    onEnd("error");
  }
  return () => {
    try {
      r.stop();
    } catch {
      /* already stopped */
    }
  };
}

export function speakSupported(): boolean {
  return "speechSynthesis" in window;
}

let speakingId: string | null = null;

/** Reads text aloud; a second call with the same id stops it. Markdown
 *  syntax and code blocks are skipped. */
export function speak(id: string, text: string, onDone: () => void) {
  if (!speakSupported()) return;
  const synth = window.speechSynthesis;
  if (speakingId === id) {
    synth.cancel();
    speakingId = null;
    onDone();
    return;
  }
  synth.cancel();
  const plain = text
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/[#>*_~|-]{1,}/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 8000);
  const u = new SpeechSynthesisUtterance(plain);
  u.lang = speechLang();
  u.onend = u.onerror = () => {
    if (speakingId === id) speakingId = null;
    onDone();
  };
  speakingId = id;
  synth.speak(u);
}

export function speaking(id: string): boolean {
  return speakingId === id;
}
