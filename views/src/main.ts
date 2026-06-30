/**
 * @file Universal labctl result View — entry point.
 *
 * Wires the MCP Apps SDK lifecycle to the shape-adaptive renderer in
 * render.ts. This is the only file that talks to the App SDK; render.ts
 * stays host-agnostic so it can also be exercised by manual/visual tests.
 *
 * Must degrade gracefully in every case: missing structuredContent, a
 * structuredContent shape that isn't the labctl wrapper, non-JSON text
 * content, a host with no theme/style vars, or a thrown error anywhere in
 * the render path. Never leave the screen blank.
 */

import {
  App,
  applyDocumentTheme,
  applyHostFonts,
  applyHostStyleVariables,
  type McpUiHostContext,
} from "@modelcontextprotocol/ext-apps";
import type { CallToolResult } from "@modelcontextprotocol/sdk/types.js";
import { renderErrorState, renderResult, renderTextResult, type RenderHost } from "./render";
import { isLabctlPayload, type LabctlPayload } from "./types";
import "./style.css";

const rootElement = document.getElementById("root");
if (!rootElement) {
  // Should be impossible given result.html, but never throw to a blank tab.
  throw new Error("labctl view: #root element missing from document");
}
// Explicit annotation (rather than relying on closure narrowing, which TS
// does not track) so every later closure sees HTMLElement, not the nullable type.
const root: HTMLElement = rootElement;

function applyHostContext(ctx: McpUiHostContext): void {
  try {
    if (ctx.theme) {
      applyDocumentTheme(ctx.theme);
    }
    if (ctx.styles?.variables) {
      applyHostStyleVariables(ctx.styles.variables);
    }
    if (ctx.styles?.css?.fonts) {
      applyHostFonts(ctx.styles.css.fonts);
    }
    if (ctx.safeAreaInsets) {
      const docStyle = document.documentElement.style;
      docStyle.setProperty("--lc-safe-top", `${ctx.safeAreaInsets.top}px`);
      docStyle.setProperty("--lc-safe-right", `${ctx.safeAreaInsets.right}px`);
      docStyle.setProperty("--lc-safe-bottom", `${ctx.safeAreaInsets.bottom}px`);
      docStyle.setProperty("--lc-safe-left", `${ctx.safeAreaInsets.left}px`);
    }
  } catch (err) {
    // Theming is cosmetic — never let it block rendering the actual result.
    console.error("labctl view: failed applying host context", err);
  }
}

function extractTextBlock(result: CallToolResult): string | undefined {
  const block = result.content?.find(
    (c): c is { type: "text"; text: string } => c.type === "text",
  );
  return block?.text;
}

/** Best-effort parse: JSON text -> value, otherwise the raw string. */
function tryParseJson(text: string): { parsed: true; value: unknown } | { parsed: false } {
  try {
    return { parsed: true, value: JSON.parse(text) };
  } catch {
    return { parsed: false };
  }
}

// 1. Create app instance.
const app = new App({ name: "labctl Result View", version: "1.0.0" });

function makeHost(): RenderHost {
  return {
    async callTool(name, args) {
      try {
        const res = await app.callServerTool({ name, arguments: args });
        if (res.isError) {
          return { ok: false, errorMessage: extractTextBlock(res) ?? `${name} returned an error.` };
        }
        const sc = res.structuredContent;
        if (isLabctlPayload(sc)) {
          return { ok: true, value: (sc as LabctlPayload).result };
        }
        if (sc !== undefined && sc !== null) {
          return { ok: true, value: sc };
        }
        const text = extractTextBlock(res);
        if (text === undefined) {
          return { ok: true, value: null };
        }
        const json = tryParseJson(text);
        return { ok: true, value: json.parsed ? json.value : text };
      } catch (err) {
        return { ok: false, errorMessage: err instanceof Error ? err.message : String(err) };
      }
    },
  };
}

function handleToolResult(result: CallToolResult): void {
  try {
    if (result.isError) {
      renderErrorState(root, new Error(extractTextBlock(result) ?? "Tool call failed."));
      return;
    }

    const structured = result.structuredContent;

    if (isLabctlPayload(structured)) {
      const payload = structured as LabctlPayload;
      renderResult(root, payload.result, payload.labctl?.ui, payload.labctl?.title, makeHost());
      return;
    }

    if (structured !== undefined && structured !== null) {
      // Some other (older/foreign) structuredContent shape — still attempt
      // shape-adaptive rendering rather than giving up.
      renderResult(root, structured, undefined, undefined, makeHost());
      return;
    }

    const text = extractTextBlock(result);
    if (text === undefined) {
      renderResult(root, null, undefined, undefined, makeHost());
      return;
    }

    const json = tryParseJson(text);
    if (json.parsed) {
      renderResult(root, json.value, undefined, undefined, makeHost());
    } else {
      renderTextResult(root, text);
    }
  } catch (err) {
    renderErrorState(root, err);
  }
}

// 2. Register ALL handlers BEFORE connecting.
app.ontoolresult = (result) => {
  handleToolResult(result);
};

app.ontoolcancelled = () => {
  renderErrorState(root, new Error("Tool call was cancelled."));
};

app.onerror = (err) => {
  console.error("labctl view: MCP Apps error", err);
};

app.onhostcontextchanged = (ctx) => {
  applyHostContext(ctx);
};

app.onteardown = async () => ({});

// 3. Connect to host.
app
  .connect()
  .then(() => {
    const ctx = app.getHostContext();
    if (ctx) {
      applyHostContext(ctx);
    }
  })
  .catch((err: unknown) => {
    console.error("labctl view: failed to connect to host", err);
    renderErrorState(root, err);
  });
