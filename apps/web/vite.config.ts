import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import { writeFileSync } from "node:fs";

// dist/.keep must survive builds: apps/web/embed.go embeds dist and needs
// at least one file to compile before the first build.
const keep = (): Plugin => ({
  name: "crewbase-keep",
  closeBundle() {
    writeFileSync(new URL("./dist/.keep", import.meta.url), "");
  },
});

const daemon = process.env.CREWD_ADDR ?? "http://127.0.0.1:7788";

export default defineConfig({
  plugins: [react(), keep()],
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 1200 },
  server: { proxy: { "/api": { target: daemon, changeOrigin: false } } },
});
