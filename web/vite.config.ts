import { readFileSync } from "node:fs";
import { defineConfig } from "vitest/config";

export default defineConfig({
  base: "/dashboard/",
  server: {
    proxy: {
      "/dashboard/api": {
        target: "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
  plugins: [
    {
      name: "runtime-license-attribution",
      generateBundle() {
        this.emitFile({
          type: "asset",
          fileName: "THIRD-PARTY-LICENSES.txt",
          source: ["react", "react-dom", "vite"]
            .map(
              (name) =>
                `${name}\n${readFileSync(new URL(`./node_modules/${name}/${name === "vite" ? "LICENSE.md" : "LICENSE"}`, import.meta.url), "utf8")}`,
            )
            .join("\n\n")
            .replace(/\r\n/g, "\n")
            .replace(/[ \t]+$/gm, ""),
        });
      },
    },
  ],
  build: {
    outDir: "../internal/dashboard/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    globals: true,
  },
});
