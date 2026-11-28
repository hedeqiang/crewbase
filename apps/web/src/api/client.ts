// Talking to crewd: the token, api() for JSON calls (direct or through the
// relay tunnel), uploads and raw file URLs.

import { useEffect, useState } from "react";
import { apiFetch, tunnelBlobURL, viaRelay } from "../tunnel";

// ---------------------------------------------------------------- token

import { type Attachment } from "./types";
const TOKEN_KEY = "crewbase.token";

export function initToken(): string | null {
  const url = new URL(window.location.href);
  const q = url.searchParams.get("token");
  if (q) {
    try {
      localStorage.setItem(TOKEN_KEY, q);
    } catch {
      /* storage unavailable: keep in memory */
    }
    memToken = q;
    url.searchParams.delete("token");
    window.history.replaceState(null, "", url.pathname + url.search + url.hash);
    return q;
  }
  return getToken();
}

let memToken: string | null = null;

export function getToken(): string | null {
  if (memToken) return memToken;
  try {
    memToken = localStorage.getItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
  return memToken;
}

export function setToken(t: string | null) {
  memToken = t;
  try {
    if (t) localStorage.setItem(TOKEN_KEY, t);
    else localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* ignore */
  }
}

// ---------------------------------------------------------------- fetch

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

let onUnauthorized: () => void = () => {};
/** What a 401 does (the handler the app set): for requests made outside api(). */
export const unauthorized = () => onUnauthorized();
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

export async function api<T = any>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await apiFetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  let data: any = null;
  const text = await res.text();
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!res.ok) {
    const e = data?.error ?? {};
    if (res.status === 401) onUnauthorized();
    throw new ApiError(res.status, e.code ?? "http_" + res.status, e.message ?? res.statusText);
  }
  return data as T;
}

/** Uploads one file (raw body) into the agent workspace. */
export async function uploadFile(agent: string, file: Blob, name: string): Promise<Attachment> {
  const headers: Record<string, string> = { "Content-Type": file.type || "application/octet-stream" };
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  const res = await apiFetch(`/api/agents/${enc(agent)}/uploads?name=${encodeURIComponent(name)}`, { method: "POST", headers, body: file });
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    if (res.status === 401) onUnauthorized();
    const e = data?.error ?? {};
    throw new ApiError(res.status, e.code ?? "http_" + res.status, e.message ?? res.statusText);
  }
  return data as Attachment;
}

/** URL of a workspace file (images inline, other files download). Over a
 *  relay the file has to come through the tunnel: use useRawURL. */
export function rawURL(agent: string, path: string): string {
  return `/api/agents/${enc(agent)}/raw?path=${encodeURIComponent(path)}&token=${encodeURIComponent(getToken() ?? "")}`;
}

/** rawURL that also works over a relay (an object URL fetched through the tunnel). */
export function useRawURL(agent: string | undefined, path: string): string | undefined {
  const direct = agent ? rawURL(agent, path) : undefined;
  const [url, setUrl] = useState<string | undefined>(viaRelay() ? undefined : direct);
  useEffect(() => {
    if (!agent || !viaRelay()) {
      setUrl(direct);
      return;
    }
    let live = true;
    let made: string | undefined;
    tunnelBlobURL(`/api/agents/${enc(agent)}/raw?path=${encodeURIComponent(path)}`, getToken() ?? "").then(
      (u) => {
        made = u;
        if (live) setUrl(u);
        else URL.revokeObjectURL(u);
      },
      () => live && setUrl(undefined),
    );
    return () => {
      live = false;
      if (made) URL.revokeObjectURL(made);
    };
  }, [agent, path]);
  return url;
}

// ---------------------------------------------------------------- path helpers (used by the API modules, not exported from ../api)

/** A path segment, escaped. */
export function enc(s: string) {
  return encodeURIComponent(s);
}

/** A team's query string, with extra parameters (empty ones left out). */
export const tq = (team: string, extra: Record<string, string | number | undefined> = {}) => {
  const p = new URLSearchParams({ team });
  for (const [k, v] of Object.entries(extra)) if (v !== undefined && v !== "") p.set(k, String(v));
  return p.toString();
};
