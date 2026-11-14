// @ts-check
// Example Crewbase plugin: interface slots. Everything it shows is
// declarative data the host renders — no UI code runs in the client.
import { definePlugin } from "@crewbase/plugin-sdk";

/** Fixed demo data (a real plugin would ask its deploy system). */
const ENVS = {
  production: { version: "v1.4.2", status: "healthy", url: "https://example.com", at: "2026-11-14 09:12" },
  staging: { version: "v1.5.0-rc.1", status: "deploying", url: "https://staging.example.com", at: "2026-11-14 15:40" },
};

definePlugin({
  name: "deploy-status",
  version: "0.1.0",
  tools: {
    deploy_status: {
      description: "Get the current deployment status of an environment (production or staging).",
      category: "read",
      params: {
        type: "object",
        properties: { env: { type: "string", enum: ["production", "staging"], description: "Environment" } },
        required: ["env"],
      },
      /** @param {{ env: string }} args */
      execute(args) {
        const env = args.env === "staging" ? "staging" : "production";
        // JSON content: the model reads it, and the conversation card renders it.
        return JSON.stringify({ env, ...ENVS[env] });
      },
    },
  },
  ui: [
    {
      slot: "conversation.node",
      key: "tool:deploy_status",
      card: {
        title: "{{result.env}} · {{result.status}}",
        tone: "ok",
        icon: "rocket",
        fields: [
          { label: "版本", value: "{{result.version}}", mono: true },
          { label: "上线时间", value: "{{result.at}}" },
          { label: "地址", value: "{{result.url}}", href: "{{result.url}}" },
        ],
      },
    },
    {
      slot: "settings.card",
      key: "deploy-overview",
      title: "部署",
      desc: "各环境当前版本（示例数据）",
      icon: "rocket",
      panel() {
        return {
          sections: [
            {
              fields: Object.entries(ENVS).map(([env, e]) => ({ label: env, value: `${e.version} · ${e.status}`, href: e.url })),
            },
          ],
        };
      },
    },
    {
      slot: "sidebar-right.tab",
      key: "deploys",
      title: "部署",
      icon: "rocket",
      panel({ session }) {
        return {
          sections: Object.entries(ENVS).map(([env, e]) => ({
            title: env,
            fields: [
              { label: "版本", value: e.version, mono: true },
              { label: "状态", value: e.status },
              { label: "地址", value: e.url, href: e.url },
            ],
          })),
          empty: session ? "" : "No deployments",
        };
      },
    },
    {
      slot: "palette.command",
      key: "deploy-refresh",
      title: "刷新部署状态",
      icon: "rocket",
      run() {
        return `production ${ENVS.production.version}，staging ${ENVS.staging.version}`;
      },
    },
    {
      slot: "composer.action",
      key: "deploy-check",
      title: "部署状态",
      desc: "查看各环境的部署状态",
      icon: "rocket",
      run() {
        return { toast: `staging 正在部署 ${ENVS.staging.version}` };
      },
    },
  ],
});
