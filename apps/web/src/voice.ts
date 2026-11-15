// Voice: dictation into the composer and reading replies aloud.
//
// With a DeepSeek key (设置 › 模型) both go through the daemon to DeepSeek
// ASR / TTS (/api/voice/*): the microphone is recorded here as 16 kHz
// 16-bit WAV (up to 2 minutes), transcribed when the person stops, and
// replies are synthesized as WAV. Without one, the browser's own Web Speech
// API and speechSynthesis are used. Nothing is sent to a model until the
// person sends the dictated text; audio is not stored.

import { getLang } from "./i18n";
import { apiFetch } from "./tunnel";
import { getToken } from "./api";

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

// ---------------------------------------------------------------- cloud

let cloud = false;
let checked = 0;

/** Refreshes whether cloud speech is available (at most once a minute). */
export async function primeVoice(force = false): Promise<boolean> {
  if (!force && Date.now() - checked < 60_000) return cloud;
  checked = Date.now();
  try {
    const res = await apiFetch("/api/voice", { headers: authHeaders() });
    cloud = res.ok && !!(await res.json())?.cloud;
  } catch {
    cloud = false;
  }
  return cloud;
}

export function cloudVoice(): boolean {
  return cloud && !!navigator.mediaDevices?.getUserMedia;
}

function authHeaders(extra: Record<string, string> = {}): Record<string, string> {
  const tok = getToken();
  return tok ? { ...extra, Authorization: `Bearer ${tok}` } : extra;
}

async function apiError(res: Response): Promise<string> {
  const data = await res.json().catch(() => null);
  return data?.error?.message || res.statusText || String(res.status);
}

const RATE = 16000;
const MAX_SECONDS = 120;

/** Encodes mono float samples as 16-bit PCM WAV. */
export function encodeWav(samples: Float32Array, rate = RATE): Blob {
  const buf = new ArrayBuffer(44 + samples.length * 2);
  const v = new DataView(buf);
  const str = (o: number, s: string) => [...s].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
  str(0, "RIFF");
  v.setUint32(4, 36 + samples.length * 2, true);
  str(8, "WAVEfmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  str(36, "data");
  v.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Blob([buf], { type: "audio/wav" });
}

/** Linear resampling to RATE. */
function resample(input: Float32Array, from: number): Float32Array {
  if (from === RATE) return input;
  const ratio = from / RATE;
  const out = new Float32Array(Math.floor(input.length / ratio));
  for (let i = 0; i < out.length; i++) {
    const x = i * ratio;
    const j = Math.floor(x);
    const f = x - j;
    out[i] = (input[j] ?? 0) * (1 - f) + (input[j + 1] ?? input[j] ?? 0) * f;
  }
  return out;
}

function cloudDictate(onText: (final: string, interim: string) => void, onEnd: (error?: string) => void): () => void {
  let stopped = false;
  let finish: () => void = () => {
    stopped = true;
  };
  (async () => {
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true } });
    } catch (e: any) {
      onEnd(e?.name === "NotAllowedError" || e?.name === "SecurityError" ? "denied" : "error");
      return;
    }
    const ac = new AudioContext();
    // created after the permission prompt, it may start suspended
    if (ac.state === "suspended") await ac.resume().catch(() => {});
    const src = ac.createMediaStreamSource(stream);
    const proc = ac.createScriptProcessor(4096, 1, 1);
    const chunks: Float32Array[] = [];
    let total = 0;
    proc.onaudioprocess = (e) => {
      const d = new Float32Array(e.inputBuffer.getChannelData(0));
      chunks.push(d);
      total += d.length;
      if (total / ac.sampleRate >= MAX_SECONDS) finish();
    };
    src.connect(proc);
    proc.connect(ac.destination);
    let done = false;
    finish = () => {
      if (done) return;
      done = true;
      proc.disconnect();
      src.disconnect();
      stream.getTracks().forEach((t) => t.stop());
      const rate = ac.sampleRate;
      void ac.close();
      const all = new Float32Array(total);
      let o = 0;
      for (const c of chunks) {
        all.set(c, o);
        o += c.length;
      }
      if (total < rate * 0.3) {
        onEnd("silence");
        return;
      }
      onText("", "…");
      const wav = encodeWav(resample(all, rate));
      apiFetch("/api/voice/transcribe", { method: "POST", headers: authHeaders({ "Content-Type": "audio/wav" }), body: wav })
        .then(async (res) => {
          if (!res.ok) throw new Error(await apiError(res));
          const text = String((await res.json())?.text ?? "").trim();
          onText(text, "");
          onEnd(text ? undefined : "silence");
        })
        .catch((e) => {
          onText("", "");
          onEnd(e?.message || "error");
        });
    };
    if (stopped) finish();
  })();
  return () => finish();
}

// ---------------------------------------------------------------- dictation

export function dictationSupported(): boolean {
  return cloudVoice() || !!ctor();
}

export function speechLang(): string {
  return getLang() === "en" ? "en-US" : "zh-CN";
}

/** Starts dictation. onText gets the final text so far plus the interim
 *  tail; onEnd is called once when it stops (error = a reason to show). */
export function dictate(onText: (final: string, interim: string) => void, onEnd: (error?: string) => void): () => void {
  if (cloudVoice()) return cloudDictate(onText, onEnd);
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

// ---------------------------------------------------------------- read aloud

export function speakSupported(): boolean {
  return cloud || "speechSynthesis" in window;
}

let speakingId: string | null = null;
let stopCurrent: (() => void) | null = null;

/** Text worth reading: code blocks, images and Markdown syntax removed. */
export function plainForSpeech(text: string): string {
  return text
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/[#>*_~|-]{1,}/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 8000);
}

function browserSpeak(id: string, plain: string, onDone: () => void) {
  if (!("speechSynthesis" in window)) {
    onDone();
    return;
  }
  const synth = window.speechSynthesis;
  const u = new SpeechSynthesisUtterance(plain);
  u.lang = speechLang();
  u.onend = u.onerror = () => {
    if (speakingId === id) speakingId = null;
    onDone();
  };
  stopCurrent = () => synth.cancel();
  synth.speak(u);
}

/** Reads text aloud; a second call with the same id stops it. */
export function speak(id: string, text: string, onDone: () => void) {
  if (!speakSupported()) return;
  const again = speakingId === id;
  stopCurrent?.();
  stopCurrent = null;
  if ("speechSynthesis" in window) window.speechSynthesis.cancel();
  if (again) {
    speakingId = null;
    onDone();
    return;
  }
  const plain = plainForSpeech(text);
  speakingId = id;
  if (!cloud) {
    browserSpeak(id, plain, onDone);
    return;
  }
  const ctl = new AbortController();
  let audio: HTMLAudioElement | null = null;
  let url = "";
  const end = () => {
    if (url) URL.revokeObjectURL(url);
    url = "";
    if (speakingId === id) speakingId = null;
    onDone();
  };
  stopCurrent = () => {
    ctl.abort();
    audio?.pause();
    if (url) URL.revokeObjectURL(url);
    url = "";
    onDone();
  };
  apiFetch("/api/voice/speak", {
    method: "POST",
    headers: authHeaders({ "Content-Type": "application/json" }),
    body: JSON.stringify({ text: plain }),
    signal: ctl.signal,
  })
    .then(async (res) => {
      if (!res.ok) throw new Error(await apiError(res));
      if (speakingId !== id) return;
      url = URL.createObjectURL(new Blob([await res.arrayBuffer()], { type: "audio/wav" }));
      audio = new Audio(url);
      audio.onended = end;
      audio.onerror = end;
      await audio.play();
    })
    .catch(() => {
      // fall back to the system voice
      if (speakingId === id && !ctl.signal.aborted) browserSpeak(id, plain, onDone);
    });
}

export function speaking(id: string): boolean {
  return speakingId === id;
}
