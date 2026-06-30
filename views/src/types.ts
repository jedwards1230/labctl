/**
 * @file Types for the labctl tool-result seam.
 *
 * This mirrors (but does not import) the Go-side wrapper produced by
 * `executeAndRender` in internal/mcpserver. See the build contract for the
 * authoritative shape:
 *
 * ```json
 * {
 *   "result": <array | object | scalar | null>,
 *   "labctl": {
 *     "service": "radarr", "command": "list", "title": "Radarr: library list",
 *     "ui": { "view": "table", "columns": ["id","title"], "primary": "title",
 *             "badges": {"monitored":"bool","hasFile":"bool"},
 *             "sort": {"by":"title","dir":"asc"}, "drilldown": "radarr_get" }
 *   }
 * }
 * ```
 *
 * Every field is treated as optional/untrusted at runtime — older or
 * malformed results must degrade gracefully, never throw.
 */

export type ViewKind = "table" | "record" | "tree";

export interface UiHints {
  view?: ViewKind;
  columns?: string[];
  primary?: string;
  /** field name -> badge style. Only "bool" is defined today. */
  badges?: Record<string, string>;
  sort?: { by?: string; dir?: "asc" | "desc" };
  /** tool/command id (same service) to call on row click, passed { id }. */
  drilldown?: string;
}

export interface LabctlMeta {
  service?: string;
  command?: string;
  title?: string;
  ui?: UiHints | null;
}

export interface LabctlPayload {
  result: unknown;
  labctl?: LabctlMeta | null;
}

/** Type guard: does this look like our wrapper object (vs. some other/older shape)? */
export function isLabctlPayload(value: unknown): value is LabctlPayload {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    "result" in (value as Record<string, unknown>)
  );
}
