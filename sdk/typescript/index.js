// @ts-check
// @crewbase/plugin-sdk — out-of-process plugins for Crewbase.
//
// The host starts the plugin as a subprocess and talks JSON-RPC 2.0 over
// stdio, one JSON message per line (protocol version 1). This module has no
// dependencies and needs no build step; types live in index.d.ts.
//
// stdout belongs to the protocol: console.log / console.info are redirected
// to stderr, which the host forwards to its log.

import { createInterface } from "node:readline";

export const PROTOCOL_VERSION = 1;

/**
 * Define a plugin. When the process was started by a Crewbase host
 * (CREWBASE_PROTOCOL is set) it starts serving stdio immediately.
 * @param {any} def
 */
export function definePlugin(def) {
  const plugin = new Plugin(def);
  if (process.env.CREWBASE_PROTOCOL) plugin.start();
  return plugin;
}

class RpcError extends Error {
  /** @param {number} code @param {string} message @param {any} [data] */
  constructor(code, message, data) {
    super(message);
    this.code = code;
    this.data = data;
  }
}
export { RpcError };

class Plugin {
  /** @param {any} def */
  constructor(def) {
    this.def = def || {};
    this.seq = 0;
    /** @type {Map<string, {resolve: (v: any) => void, reject: (e: any) => void}>} */
    this.pending = new Map();
    this.out = process.stdout;
    this.started = false;
    /** @type {any} */
    this.init = null;
    const self = this;
    /** Host services available to handlers. */
    this.host = {
      /** @param {string} message @param {string} [level] */
      log(message, level = "info") {
        self.notify("log", { level, message: String(message) });
      },
      /** @param {any} n */
      notify(n) {
        return self.call("notify.send", n);
      },
      /** @param {string} ref */
      async resolveCredential(ref) {
        const r = await self.call("credentials.resolve", { ref });
        return r && r.value;
      },
      get permissions() {
        return (self.init && self.init.permissions) || {};
      },
      get config() {
        return (self.init && self.init.config) || {};
      },
    };
  }

  start() {
    if (this.started) return;
    this.started = true;
    // Keep stdout clean for the protocol.
    const toErr = (/** @type {any[]} */ ...a) => process.stderr.write(a.map(String).join(" ") + "\n");
    console.log = toErr;
    console.info = toErr;
    const rl = createInterface({ input: process.stdin, crlfDelay: Infinity });
    rl.on("line", (line) => {
      line = line.trim();
      if (!line) return;
      let msg;
      try {
        msg = JSON.parse(line);
      } catch {
        return;
      }
      this.dispatch(msg);
    });
    rl.on("close", () => process.exit(0));
  }

  /** @param {any} msg */
  write(msg) {
    this.out.write(JSON.stringify({ jsonrpc: "2.0", ...msg }) + "\n");
  }

  /** @param {string} method @param {any} params */
  notify(method, params) {
    this.write({ method, params });
  }

  /** @param {string} method @param {any} params @returns {Promise<any>} */
  call(method, params) {
    const id = String(++this.seq);
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.write({ id, method, params });
    });
  }

  /** @param {any} msg */
  async dispatch(msg) {
    const hasId = msg.id !== undefined && msg.id !== null;
    if (msg.method && hasId) {
      try {
        const result = await this.handle(msg.method, msg.params || {});
        this.write({ id: msg.id, result: result === undefined ? null : result });
      } catch (e) {
        const err = /** @type {any} */ (e);
        const code = typeof err?.code === "number" ? err.code : -32603;
        this.write({ id: msg.id, error: { code, message: String(err?.message ?? err), data: err?.data } });
      }
      return;
    }
    if (msg.method) {
      try {
        await this.handleNotification(msg.method, msg.params || {});
      } catch (e) {
        process.stderr.write(`notification ${msg.method} failed: ${e}\n`);
      }
      return;
    }
    if (hasId) {
      const p = this.pending.get(String(msg.id));
      if (!p) return;
      this.pending.delete(String(msg.id));
      if (msg.error) p.reject(new RpcError(msg.error.code, msg.error.message, msg.error.data));
      else p.resolve(msg.result);
    }
  }

  tools() {
    const t = this.def.tools || {};
    return Array.isArray(t) ? t : Object.entries(t).map(([name, v]) => ({ name, ...v }));
  }

  registrations() {
    const d = this.def;
    const events = [];
    if (d.onToolResult) events.push("tools/result");
    if (d.preExecute) events.push("tools/pre-execute");
    if (d.postExecute) events.push("tools/post-execute");
    let prompts = d.prompts || [];
    if (!Array.isArray(prompts)) prompts = Object.entries(prompts).map(([name, text]) => ({ name, text }));
    return {
      protocolVersion: PROTOCOL_VERSION,
      name: d.name,
      version: d.version,
      tools: this.tools().map((t) => ({
        name: t.name,
        description: t.description || "",
        params: t.params || { type: "object", properties: {} },
        category: t.category || "none",
        timeoutMs: t.timeoutMs || 0,
      })),
      prompts,
      events,
      notify: Object.keys(d.notify || {}).map((name) => ({ name })),
    };
  }

  /** @param {string} method @param {any} p */
  async handle(method, p) {
    const d = this.def;
    switch (method) {
      case "initialize":
        this.init = p;
        if (d.setup) await d.setup(this.host);
        return this.registrations();
      case "tool/execute": {
        const tool = this.tools().find((t) => t.name === p.tool);
        if (!tool) throw new RpcError(-32601, `unknown tool ${p.tool}`);
        const out = await tool.execute(p.call.args || {}, { call: p.call, host: this.host });
        if (typeof out === "string") return { content: out };
        if (out == null) return { content: "" };
        return out;
      }
      case "tools/pre-execute":
        return (await d.preExecute?.(p, this.host)) ?? null;
      case "tools/post-execute":
        return (await d.postExecute?.(p, this.host)) ?? null;
      case "notify/send": {
        const ch = d.notify?.[p.channel];
        if (!ch) throw new RpcError(-32601, `unknown channel ${p.channel}`);
        await ch(p.notification, this.host);
        return null;
      }
    }
    throw new RpcError(-32601, `method not found: ${method}`);
  }

  /** @param {string} method @param {any} p */
  async handleNotification(method, p) {
    switch (method) {
      case "tools/result":
        await this.def.onToolResult?.(p, this.host);
        return;
      case "shutdown":
        await this.def.dispose?.();
        process.exit(0);
    }
  }
}
