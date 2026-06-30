import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vite";
import { viteSingleFile } from "vite-plugin-singlefile";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Emit straight into the Go package that //go:embed's it, so a plain
// `go build` in the parent repo always has a built artifact available.
const GO_EMBED_DIR = path.resolve(__dirname, "../internal/mcpserver/views");

export default defineConfig({
  plugins: [viteSingleFile()],
  build: {
    target: "es2020",
    cssMinify: true,
    minify: true,
    rollupOptions: {
      // Entry is named result.html (not index.html) so vite's HTML-derived
      // output filename is already internal/mcpserver/views/result.html —
      // no postbuild rename needed.
      input: path.resolve(__dirname, "result.html"),
    },
    outDir: GO_EMBED_DIR,
    // Never wipe internal/mcpserver/views/ — the Go worker's views.go
    // (//go:embed result.html) lives alongside the built HTML.
    emptyOutDir: false,
  },
});
