// Webhooks, the network proxy, Open Agent Docs, add-on updates, devices and
// members, the task board, pairing and the relay, IM gateways.

// ---------------------------------------------------------------- webhooks (features/webhooks)

export type WhTargetKind = "workflow" | "routine" | "channel";
export interface WhTarget {
  kind: WhTargetKind;
  id: string;
  mention?: string;
}
export interface WhEndpoint {
  id: string;
  team: string;
  name: string;
  provider: string;
  secret: string;
  enabled: boolean;
  events?: string[];
  filter?: string;
  instruction?: string;
  targets: WhTarget[];
  cooldown?: number;
  cooldownKey?: string;
  rateLimit?: number;
  mode?: string;
  relay?: boolean;
  relayToken?: string;
  created: string;
  updated: string;
}
export interface WhSubscription {
  id: string;
  team: string;
  name: string;
  url: string;
  events: string[];
  filter?: string;
  secret: string;
  enabled: boolean;
  created: string;
  updated: string;
}
export interface WhDelivery {
  id: string;
  dir: "in" | "out";
  team: string;
  endpoint?: string;
  subscription?: string;
  name: string;
  event?: string;
  key?: string;
  via?: string;
  status: string;
  error?: string;
  code?: number;
  attempts?: number;
  next?: string;
  targets?: (WhTarget & { ok: boolean; error?: string; ref?: string; detail?: string })[];
  prompt?: string;
  headers?: Record<string, string>;
  body?: unknown;
  bodyText?: string;
  at: string;
  updated: string;
}
/** Settings › 网络代理 (features/proxy). The password is never sent back:
 *  hasPassword says the vault has one. */
export type ProxyType = "http" | "https" | "socks5";
export interface ProxySettings {
  enabled: boolean;
  type: ProxyType;
  host: string;
  port: number;
  username?: string;
  hasPassword: boolean;
  bypass: string[];
}
/** a change: password "" keeps the saved one */
export interface ProxyInput extends ProxySettings {
  password?: string;
  clearPassword?: boolean;
}
export interface ProxyView {
  settings: ProxySettings;
  /** the environment's proxy, used while off */
  env?: string;
}
export interface ProxyCheck {
  ok: boolean;
  ms: number;
  status?: number;
  code?: "unreachable" | "auth" | "timeout" | "tls" | "type" | "status" | "other";
  error?: string;
  target: string;
}
export interface ProxyFound {
  type: ProxyType;
  host: string;
  port: number;
  source: "system" | "env" | "port";
  /** socks5: the port speaks both */
  also?: string;
}

export interface WhSettings {
  allowPrivate: string[];
  maxAttempts: number;
}
export type WhEndpointInput = Partial<Omit<WhEndpoint, "id" | "team" | "secret" | "created" | "updated" | "relayToken">> & { secret?: string };
export type WhSubscriptionInput = Partial<Pick<WhSubscription, "name" | "url" | "events" | "filter" | "enabled">> & { secret?: string };


export interface DocPageCard {
  id: string;
  url: string;
  title: string;
  icon?: string;
  updated?: number;
  markdown: string;
  total: number;
  shown: number;
}

/** Open Agent Docs connection (features/agentdocs). */
export interface AgentDocsStatus {
  connected: boolean;
  settings: { url: string; workspace: string; workspaceName?: string; agents?: string[]; scope?: string; wake: boolean };
  identities: { agent: string; id: string; name: string; after?: number }[];
  error?: string;
  /** the admin token is gone from the vault: repairAgentDocs fixes it */
  needsAdmin?: boolean;
  woken: number;
  /** the server Crewbase runs (一键启动) */
  local: { supported: boolean; release: string; running: boolean; url?: string; email?: string; version?: string; step?: string; error?: string };
}

/** An available update of an add-on (features/addonupdates). */
export interface AddonUpdate {
  kind: "plugin" | "skill" | "app";
  id: string;
  agent?: string;
  name: string;
  desc?: string;
  /** plugins only: apps and skills show no version */
  current?: string;
  latest?: string;
  notes?: string;
  source?: string;
  /** the update asks for more; it waits for approval */
  newPermissions?: string[];
}

export interface AddonUpdates {
  items: AddonUpdate[];
  checked?: string;
  error?: string;
}

export interface DeviceView {
  id: string;
  name: string;
  tier: "mobile" | "desktop";
  platform?: string;
  created: string;
  lastSeen?: string;
  via?: "lan" | "relay";
  member?: string;
}

export interface MemberView {
  id: string;
  name: string;
  role: "admin" | "member" | "viewer";
  approve: string[];
  created: string;
  devices?: number;
}

export interface TaskView {
  id: string;
  title: string;
  detail?: string;
  owner?: string;
  status: "todo" | "doing" | "blocked" | "done";
  deps?: string[];
  scope?: string[];
  note?: string;
  by: string;
  created: string;
  updated: string;
  ready: boolean;
  waiting?: string[];
}

export interface BoardView {
  root: string;
  tasks: TaskView[];
}

export interface TaskChange {
  title?: string;
  detail?: string;
  owner?: string;
  status?: string;
  deps?: string[];
  scope?: string[];
  note?: string;
}

export interface PairingCode {
  code: string;
  tier: string;
  expires: string;
}

export interface RelayStatus {
  configured: boolean;
  hostId?: string;
  status?: { relay: string; hostId: string; online: boolean; devices: number; error?: string };
}

export interface ImPlatform {
  id: string;
  name: string;
  nameEn?: string;
  channelEn?: string;
  app: boolean;
  appId: boolean;
  channel: string;
  tokenDoc: string;
  domain?: string;
  /** 飞书 / Lark: one product, two open platforms (the first is the default). */
  regions?: ImRegion[];
}

export interface ImRegion {
  id: string;
  name: string;
  nameEn?: string;
  brand: string;
  brandEn?: string;
  domain: string;
  tokenDoc: string;
}

export interface ImGateway {
  id: string;
  platform: string;
  channel: string;
  allowUsers: string[];
  appId?: string;
  region?: string;
  relayReplies: boolean;
  approvals: boolean;
  enabled: boolean;
  hasToken: boolean;
  hasAppToken?: boolean;
  connected?: boolean;
  running: boolean;
  error?: string;
  listen?: { agent: string; keywords?: string[] } | null;
  team?: string;
}

export type Followup = { id: string; agent: string; session: string; note: string; due: string; set: string };
export type WorkTask = { name: string; title: string; updated: string };
export type WorkNote = { name: string; title: string; size: number; updated: string };
export type ToldEntry = { time: string; session: string; via: string; text: string };
export type Working = { tasks: WorkTask[]; notes: WorkNote[]; told: ToldEntry[] };
