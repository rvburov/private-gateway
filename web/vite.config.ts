import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// Версия worker меняется с файлами оболочки; приватные маршруты в этот список не входят.
function gatewayPwa(): Plugin {
  let root = "";
  const publicFiles = ["manifest.webmanifest", "offline.html", "icons/icon.svg", "icons/icon-192.png", "icons/icon-512.png", "icons/maskable-512.png", "icons/apple-touch-icon.png", "icons/badge-96.png"];
  return {
    name: "private-gateway-pwa",
    apply: "build",
    configResolved(config) { root = config.root; },
    generateBundle: { order: "post", handler(_options, bundle) {
      const files = Object.values(bundle).filter((file) => file.fileName === "index.html" || file.fileName.startsWith("assets/"));
      if (!files.some((file) => file.fileName === "index.html")) this.error("Нет index.html для офлайн-оболочки приложения.");
      const worker = readFileSync(resolve(root, "src/pwa/sw.js"), "utf8");
      const hash = createHash("sha256").update(worker);
      for (const file of [...files].sort((a, b) => a.fileName.localeCompare(b.fileName))) {
        hash.update(file.fileName).update(file.type === "chunk" ? file.code : file.source);
      }
      for (const name of publicFiles) hash.update(name).update(readFileSync(resolve(root, "public", name)));
      const build = { version: hash.digest("hex").slice(0, 20), shell: [...files.map((file) => "/" + file.fileName), ...publicFiles.map((name) => "/" + name)] };
      this.emitFile({ type: "asset", fileName: "sw.js", source: `self.__GATEWAY_PWA__ = ${JSON.stringify(build)};\n${worker}` });
    } },
  };
}

export default defineConfig({
  plugins: [react(), gatewayPwa()],
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/healthz": "http://127.0.0.1:8080",
      "/readyz": "http://127.0.0.1:8080",
    },
  },
});
