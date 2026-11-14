// Types for @crewbase/plugin-sdk (protocol version 1).

export declare const PROTOCOL_VERSION: 1;

/** JSON Schema object describing tool parameters. */
export type JSONSchema = { type: "object"; properties?: Record<string, unknown>; required?: string[]; [k: string]: unknown };

/** Action categories used by the rules engine. */
export type Category = "read" | "exec" | "install" | "write" | "push" | "delete" | "publish" | "payment" | "none";

export interface ToolCall {
  id: string;
  name: string;
  args: Record<string, unknown>;
  agent?: string;
  mode?: string;
  workspace?: string;
}

export interface ToolResult {
  content: string;
  isError?: boolean;
  data?: unknown;
}

/** Granted permission set, as approved by the user. */
export interface Permissions {
  services?: string[];
  events?: string[];
  network?: string[];
  credentials?: string[];
  files?: string[];
  subprocess?: boolean;
}

export interface Notification {
  kind?: string;
  title: string;
  body?: string;
  agent?: string;
  session?: string;
  urgent?: boolean;
}

/** Host services. Each call is checked against the granted permissions. */
export interface Host {
  log(message: string, level?: "debug" | "info" | "warn" | "error"): void;
  /** Requires service "notify". */
  notify(n: Notification): Promise<{ delivered: number; errors?: string[] }>;
  /** Requires service "credentials" and the ref listed in permissions.credentials. */
  resolveCredential(ref: string): Promise<string>;
  readonly permissions: Permissions;
  readonly config: Record<string, unknown>;
}

export interface ToolContext {
  call: ToolCall;
  host: Host;
}

export interface ToolDefinition<A = any> {
  description: string;
  params?: JSONSchema;
  category?: Category;
  /** Per-call timeout in ms (host default 60s). */
  timeoutMs?: number;
  execute(args: A, ctx: ToolContext): string | ToolResult | void | Promise<string | ToolResult | void>;
}

/** Finalized tool execution (tools/result, tools/post-execute). */
export interface Execution {
  call: ToolCall;
  category: string;
  result: ToolResult;
  denied?: boolean;
}

/** Pre-execute decision as seen by a plugin. */
export interface Decision {
  call: ToolCall;
  category: string;
  summary: string;
  verdict: "allow" | "ask" | "deny";
  reason?: string;
}

/** A plugin may only deny or annotate; it can never turn a deny into an allow. */
export type PreExecuteResult = { verdict?: "deny"; reason?: string; summary?: string } | null | undefined | void;

export type PostExecuteResult = { content?: string; isError?: boolean } | null | undefined | void;

/** A label / value row. In cards, value and href may use {{path}} into the event data. Always plain text. */
export interface UIField {
  label: string;
  value: string;
  /** http(s) only */
  href?: string;
  mono?: boolean;
}

export interface UIPanel {
  sections: { title?: string; text?: string; fields?: UIField[] }[];
  empty?: string;
}

/** Interface contributions (requires service "ui"). Declarative: the host renders them. */
export type UIContribution =
  | {
      /** A card for session events of type `key` (e.g. "deploy/result"). */
      slot: "conversation.node";
      key: string;
      card: { title: string; tone?: "info" | "ok" | "warn" | "error"; icon?: string; fields?: UIField[] };
    }
  | {
      slot: "sidebar-right.tab" | "settings.card";
      key: string;
      title: string;
      desc?: string;
      icon?: string;
      order?: number;
      /** session is the open conversation for tabs, "" for settings cards. */
      panel(ctx: { session: string }, host: Host): UIPanel | Promise<UIPanel>;
    }
  | {
      slot: "palette.command" | "composer.action";
      key: string;
      title: string;
      desc?: string;
      icon?: string;
      order?: number;
      /** Asked before running. */
      confirm?: string;
      run(ctx: { session: string }, host: Host): string | { toast?: string; open?: string } | void | Promise<string | { toast?: string; open?: string } | void>;
    };

export interface PluginDefinition {
  name?: string;
  version?: string;
  tools?: Record<string, ToolDefinition>;
  /** Static system prompt fragments (requires service "prompt"). */
  prompts?: Record<string, string> | { name: string; text: string; order?: number }[];
  /** tools/result (Emit, fire and forget; requires event permission). */
  onToolResult?(exe: Execution, host: Host): void | Promise<void>;
  /** tools/pre-execute (Waterfall, 2 s timeout → unchanged). */
  preExecute?(d: Decision, host: Host): PreExecuteResult | Promise<PreExecuteResult>;
  /** tools/post-execute (Waterfall, 2 s timeout → unchanged). */
  postExecute?(exe: Execution, host: Host): PostExecuteResult | Promise<PostExecuteResult>;
  /** Notification channels by name (requires service "notify"). */
  notify?: Record<string, (n: Notification, host: Host) => void | Promise<void>>;
  /** Interface contributions (requires service "ui"). */
  ui?: UIContribution[];
  setup?(host: Host): void | Promise<void>;
  dispose?(): void | Promise<void>;
}

export declare class RpcError extends Error {
  code: number;
  data?: unknown;
  constructor(code: number, message: string, data?: unknown);
}

export interface PluginHandle {
  readonly host: Host;
  /** Start serving stdio (automatic when launched by a Crewbase host). */
  start(): void;
}

export declare function definePlugin(def: PluginDefinition): PluginHandle;
