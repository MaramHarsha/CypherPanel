import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";

// Dev proxies /api to a locally running cypherd; production is served BY
// cypherd (go:embed), so the app always talks to its own origin.
export default defineConfig({
  plugins: [
    tanstackRouter({ target: "react", autoCodeSplitting: true }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  build: {
    // Route-level code splitting is on (autoCodeSplitting); keep chunks honest.
    chunkSizeWarningLimit: 300,
    // Never inline fonts. cypherd serves `font-src 'self'` (web-ui-design.md
    // §5), so a small font emitted as a `data:` URI is silently blocked at
    // runtime and the page falls back to a system face. Emitting every font as
    // a real file keeps the strict CSP intact instead of widening it to
    // `data:` for the sake of one subset.
    assetsInlineLimit: (filePath) => (/\.(woff2?|ttf|otf|eot)$/i.test(filePath) ? false : undefined),
  },
});
