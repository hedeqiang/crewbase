// 文档库 (features/library): items, links, versions and their raw files.

import { useEffect, useState } from "react";
import { apiFetch, tunnelBlobURL, viaRelay } from "../tunnel";

// ---------------------------------------------------------------- 文档库

import { ApiError, getToken, unauthorized } from "./client";
export type LibFolder = "inbox" | "media" | "docs" | "pages";
export type LibSource = "upload" | "generated" | "agent" | "person";

/** A document of the team library (features/library). */
export interface LibItem {
  id: number;
  team: string;
  folder: LibFolder;
  name: string;
  title?: string;
  mime: string;
  size: number;
  sha256: string;
  source: LibSource;
  agent?: string;
  session?: string;
  tags?: string[];
  private?: boolean;
  note?: string;
  created: string;
  updated: string;
  text: boolean;
  versions?: number;
  /** search hit: the matching passage, the match in «» */
  snippet?: string;
  /** search hit: where in the document (section / page) */
  passage?: { start: number; heading?: string };
  /** search hit: only its summary or keywords matched */
  bySummary?: boolean;
  /** written by a model when summaries are on (item detail) */
  summary?: string;
  keywords?: string[];
}

/** A document's tie to the workspace file it was published from. */
export interface LibLink {
  item: number;
  agent: string;
  path: string;
  /** the conversation (and message) it was published from */
  session?: string;
  seq?: number;
  /** when the two last matched */
  at: string;
  /** same | file (the workspace is newer) | doc (the library is newer) | both | file-gone; "" another member's agent */
  state: "same" | "file" | "doc" | "both" | "file-gone" | "";
}

export interface LibFileLink extends LibLink {
  name: string;
  title?: string;
}


export interface LibStats {
  total: number;
  folders: Partial<Record<LibFolder, number>>;
  sources: Partial<Record<LibSource, number>>;
  tags: { tag: string; n: number }[];
}

export interface LibVersion {
  id: number;
  size: number;
  agent?: string;
  source: string;
  at: string;
}

export interface LibQuery {
  q?: string;
  folder?: string;
  source?: string;
  tag?: string;
  limit?: number;
  offset?: number;
}

export function libQS(team: string, extra: Record<string, string | number | undefined> = {}): string {
  const p = new URLSearchParams({ team });
  for (const [k, v] of Object.entries(extra)) if (v !== undefined && v !== "") p.set(k, String(v));
  return p.toString();
}

/** URL of a library file (or an earlier version); see useLibRawURL for relays. */
export function libRawURL(team: string, id: number, version?: number): string {
  return `/api/library/${id}/raw?${libQS(team, { version, token: getToken() ?? "" })}`;
}

/** libRawURL that also works over a relay. */
export function useLibRawURL(team: string, id: number, version?: number): string | undefined {
  const direct = libRawURL(team, id, version);
  const [url, setUrl] = useState<string | undefined>(viaRelay() ? undefined : direct);
  useEffect(() => {
    if (!viaRelay()) {
      setUrl(direct);
      return;
    }
    let live = true;
    let made: string | undefined;
    tunnelBlobURL(`/api/library/${id}/raw?${libQS(team, { version })}`, getToken() ?? "").then(
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
  }, [team, id, version]);
  return url;
}

/** The text of a library file as stored (Markdown, code, …). */
export async function libRawText(team: string, id: number, version?: number): Promise<string> {
  const headers: Record<string, string> = {};
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  const res = await apiFetch(`/api/library/${id}/raw?${libQS(team, { version })}`, { headers });
  if (!res.ok) throw new ApiError(res.status, "http_" + res.status, res.statusText);
  return res.text();
}

/** Adds a file to the library (the person's own). */
export async function uploadLibrary(team: string, file: Blob, name: string, folder?: string): Promise<LibItem> {
  const headers: Record<string, string> = { "Content-Type": file.type || "application/octet-stream" };
  const tok = getToken();
  if (tok) headers.Authorization = `Bearer ${tok}`;
  const res = await apiFetch(`/api/library?${libQS(team, { name, folder })}`, { method: "POST", headers, body: file });
  const data = await res.json().catch(() => null);
  if (!res.ok) {
    if (res.status === 401) unauthorized();
    const e = data?.error ?? {};
    throw new ApiError(res.status, e.code ?? "http_" + res.status, e.message ?? res.statusText);
  }
  return data as LibItem;
}

/** The start of a docs page, for a card in a conversation. */
