import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import { cpSync, createReadStream, existsSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";

// dist/.keep must survive builds: apps/web/embed.go embeds dist and needs
// at least one file to compile before the first build.
const keep = (): Plugin => ({
  name: "crewbase-keep",
  closeBundle() {
    writeFileSync(new URL("./dist/.keep", import.meta.url), "");
  },
});

// pdf.js's fonts, CJK character maps and image decoders: served from
// /pdfjs/ (copied into dist; read from node_modules by the dev server).
// QuickJS (PDF scripts) is left out: scripts never run here.
const pdfjsDir = new URL("./node_modules/pdfjs-dist/", import.meta.url).pathname;
const pdfjsParts = ["cmaps", "standard_fonts", "wasm"];
const keepPdfPart = (p: string) => !/quickjs/i.test(p);
const pdfjsAssets = (): Plugin => ({
  name: "crewbase-pdfjs-assets",
  configureServer(server) {
    server.middlewares.use("/pdfjs/", (req, res, next) => {
      const rel = decodeURIComponent((req.url ?? "").split("?")[0]).replace(/^\/+/, "");
      const [part] = rel.split("/");
      const file = join(pdfjsDir, rel);
      if (!pdfjsParts.includes(part) || rel.includes("..") || !keepPdfPart(rel) || !existsSync(file) || !statSync(file).isFile()) return next();
      if (file.endsWith(".wasm")) res.setHeader("Content-Type", "application/wasm");
      createReadStream(file).pipe(res);
    });
  },
  closeBundle() {
    for (const part of pdfjsParts)
      cpSync(join(pdfjsDir, part), new URL(`./dist/pdfjs/${part}`, import.meta.url).pathname, { recursive: true, filter: (src) => keepPdfPart(src) && !/LICENSE_QUICKJS/.test(src) });
  },
});

const daemon = process.env.CREWD_ADDR ?? "http://127.0.0.1:7788";

export default defineConfig({
  plugins: [react(), keep(), pdfjsAssets()],
  build: { outDir: "dist", emptyOutDir: true, chunkSizeWarningLimit: 1200 },
  server: { proxy: { "/api": { target: daemon, changeOrigin: false } } },
});
