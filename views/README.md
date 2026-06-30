# views/ — labctl MCP Apps result View

A small Vite + TypeScript, vanilla-JS (no framework) project that builds the
single self-contained HTML file labctl serves as its universal MCP Apps
result View: a shape-adaptive table / record / tree renderer for any read
tool's `structuredContent`. See `internal/mcpserver/views/views.go` for how
the Go side embeds and serves it, and the MCP section of the repo
`CLAUDE.md`/README for the wire contract.

## Build

```bash
cd views
npm install
npm run build
```

`npm run build` runs `tsc --noEmit` (typecheck) then `vite build`, which
inlines all JS/CSS into one file via `vite-plugin-singlefile` and writes it
straight to:

```
internal/mcpserver/views/result.html
```

(`vite.config.ts` sets `build.outDir` to that path and `emptyOutDir: false`
so it never touches `views.go`/`views_test.go` living alongside it.) A plain
`go build` never needs npm — the built `result.html` is committed.

## Dev loop

```bash
npm run dev      # vite dev server, for iterating on src/ with HMR
npm run watch    # vite build --watch, rebuilds result.html on save
```

For an end-to-end loop against a live `labctl mcp --http` server, set
`LABCTL_VIEWS_DIR=$PWD` (mirrors `LABCTL_CONFIG_DIR`) so the Go binary reads
`result.html` straight off disk instead of the embedded copy — no Go rebuild
needed while iterating on `views/`.

## Layout

- `result.html` — the Vite entry point (named to match the build's output
  filename — no postbuild rename needed).
- `src/main.ts` — MCP Apps SDK lifecycle: registers `ontoolresult` and the
  host-theme handlers before `app.connect()`, extracts the labctl result
  wrapper from `structuredContent` (falling back to text content, then to a
  plain empty state — never throws to a blank screen).
- `src/render.ts` — pure, host-agnostic shape-adaptive renderer (table /
  record / tree). Takes a small `RenderHost` abstraction for the table
  drilldown feature instead of depending on the SDK directly, so it can be
  exercised by other harnesses without a real MCP Apps host.
- `src/types.ts` — the `LabctlPayload`/`UiHints` types mirroring the Go-side
  `structuredContent` wrapper (`{ result, labctl: { service, command, title,
  ui } }`).
- `src/style.css` — styling via CSS custom properties the host sets
  (`applyHostStyleVariables`), each with a sane standalone fallback.

## SDK

Built on `@modelcontextprotocol/ext-apps` (vanilla-JS `App` class, no
framework) — see `/tmp/mcp-ext-apps/examples/basic-server-vanillajs` for the
upstream reference this project is based on. The UI resource MIME type the
host expects is `RESOURCE_MIME_TYPE` from `@modelcontextprotocol/ext-apps/server`,
currently `"text/html;profile=mcp-app"`.
