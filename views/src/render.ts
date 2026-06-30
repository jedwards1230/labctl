/**
 * @file Pure, host-agnostic rendering logic for the universal labctl result
 * View: shape-adaptive table / record / tree renderers.
 *
 * Nothing in this module talks to the MCP Apps SDK directly — it only needs
 * a DOM container, the already-extracted `result` value, optional `ui` hints,
 * and a small `RenderHost` abstraction for the table drilldown feature. That
 * keeps it reusable for both the real app (main.ts, wired to app.connect())
 * and ad-hoc manual/visual testing with mocked data.
 */

import type { UiHints, ViewKind } from "./types";

export interface DrilldownOutcome {
  ok: boolean;
  /** The record-ish value to render on success (already unwrapped). */
  value?: unknown;
  errorMessage?: string;
}

export interface RenderHost {
  /** Call a server tool by id with the given arguments. Must never throw. */
  callTool(name: string, args: Record<string, unknown>): Promise<DrilldownOutcome>;
}

const NOOP_HOST: RenderHost = {
  async callTool() {
    return { ok: false, errorMessage: "No host available for drilldown." };
  },
};

export function renderResult(
  container: HTMLElement,
  result: unknown,
  hints: UiHints | null | undefined,
  title: string | undefined,
  host: RenderHost = NOOP_HOST,
): void {
  container.innerHTML = "";
  try {
    const wrapper = document.createElement("div");
    wrapper.className = "lc-view";

    if (title) {
      const h = document.createElement("h1");
      h.className = "lc-title";
      h.textContent = title;
      wrapper.appendChild(h);
    }

    wrapper.appendChild(renderBody(result, hints ?? undefined, host));
    container.appendChild(wrapper);
  } catch (err) {
    renderErrorState(container, err);
  }
}

/**
 * Fallback for when there's no structuredContent at all (older tool result)
 * and the text content isn't JSON — just show it as readable text instead of
 * guessing at structure.
 */
export function renderTextResult(container: HTMLElement, text: string, title?: string): void {
  container.innerHTML = "";
  const wrapper = document.createElement("div");
  wrapper.className = "lc-view";

  if (title) {
    const h = document.createElement("h1");
    h.className = "lc-title";
    h.textContent = title;
    wrapper.appendChild(h);
  }

  const pre = document.createElement("pre");
  pre.className = "lc-json";
  pre.style.whiteSpace = "pre-wrap";
  pre.textContent = text;
  wrapper.appendChild(pre);

  container.appendChild(wrapper);
}

export function renderErrorState(container: HTMLElement, err: unknown): void {
  container.innerHTML = "";
  const box = document.createElement("div");
  box.className = "lc-error";

  const msg = document.createElement("div");
  msg.textContent = "Couldn't render this result.";
  box.appendChild(msg);

  const detail = document.createElement("pre");
  detail.textContent = err instanceof Error ? err.message : String(err);
  box.appendChild(detail);

  container.appendChild(box);
}

function renderEmptyState(message: string): HTMLElement {
  const el = document.createElement("div");
  el.className = "lc-empty";
  el.textContent = message;
  return el;
}

// ---------------------------------------------------------------------------
// Shape detection
// ---------------------------------------------------------------------------

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isArrayOfObjects(value: unknown): value is Record<string, unknown>[] {
  return Array.isArray(value) && value.length > 0 && value.every(isPlainObject);
}

function resolveViewKind(result: unknown, hints?: UiHints): ViewKind {
  if (hints?.view === "table" || hints?.view === "record" || hints?.view === "tree") {
    return hints.view;
  }
  if (Array.isArray(result)) {
    return isArrayOfObjects(result) || result.length === 0 ? "table" : "tree";
  }
  if (isPlainObject(result)) {
    return "record";
  }
  return "tree";
}

function renderBody(result: unknown, hints: UiHints | undefined, host: RenderHost): HTMLElement {
  if (result === null || result === undefined) {
    return renderEmptyState("No data.");
  }

  const view = resolveViewKind(result, hints);

  switch (view) {
    case "table": {
      if (!Array.isArray(result)) {
        // Hint forced "table" on a non-array — degrade to tree rather than crash.
        return renderTree(result);
      }
      return renderTable(result, hints, host);
    }
    case "record": {
      if (!isPlainObject(result)) {
        return renderTree(result);
      }
      return renderRecord(result);
    }
    case "tree":
    default:
      return renderTree(result);
  }
}

// ---------------------------------------------------------------------------
// Table view
// ---------------------------------------------------------------------------

function unionKeys(rows: Record<string, unknown>[]): string[] {
  const seen = new Set<string>();
  const order: string[] = [];
  for (const row of rows) {
    for (const key of Object.keys(row)) {
      if (!seen.has(key)) {
        seen.add(key);
        order.push(key);
      }
    }
  }
  return order;
}

function isIdLikeColumn(col: string): boolean {
  return /(^id$|_id$|^uuid$|^guid$)/i.test(col);
}

function isBadgeColumn(col: string, hints: UiHints | undefined): boolean {
  return hints?.badges?.[col] === "bool";
}

function cellValue(row: Record<string, unknown>, col: string): unknown {
  return row[col];
}

function formatScalar(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function renderTable(rows: unknown[], hints: UiHints | undefined, host: RenderHost): HTMLElement {
  const objRows = rows.filter(isPlainObject);

  if (objRows.length === 0) {
    return renderEmptyState("No results.");
  }

  const columns =
    hints?.columns && hints.columns.length > 0 ? hints.columns : unionKeys(objRows);

  const root = document.createElement("div");

  const toolbar = document.createElement("div");
  toolbar.className = "lc-toolbar";

  const filterInput = document.createElement("input");
  filterInput.type = "search";
  filterInput.placeholder = "Filter…";
  filterInput.className = "lc-filter";
  filterInput.setAttribute("aria-label", "Filter rows");

  const count = document.createElement("span");
  count.className = "lc-count";

  toolbar.appendChild(filterInput);
  toolbar.appendChild(count);
  root.appendChild(toolbar);

  const tableWrap = document.createElement("div");
  tableWrap.className = "lc-table-wrap";
  root.appendChild(tableWrap);

  const table = document.createElement("table");
  table.className = "lc-table";
  tableWrap.appendChild(table);

  const thead = document.createElement("thead");
  const headRow = document.createElement("tr");
  table.appendChild(thead);
  thead.appendChild(headRow);

  const tbody = document.createElement("tbody");
  table.appendChild(tbody);

  let sortBy = hints?.sort?.by && columns.includes(hints.sort.by) ? hints.sort.by : undefined;
  let sortDir: "asc" | "desc" = hints?.sort?.dir === "desc" ? "desc" : "asc";
  let filterText = "";
  let expandedRowIndex: number | null = null;

  const ths = new Map<string, HTMLTableCellElement>();
  for (const col of columns) {
    const th = document.createElement("th");
    th.dataset.col = col;
    const label = document.createElement("span");
    label.textContent = col;
    th.appendChild(label);
    const arrow = document.createElement("span");
    arrow.className = "lc-sort-arrow";
    th.appendChild(arrow);
    th.addEventListener("click", () => {
      if (sortBy === col) {
        sortDir = sortDir === "asc" ? "desc" : "asc";
      } else {
        sortBy = col;
        sortDir = "asc";
      }
      expandedRowIndex = null;
      renderRows();
    });
    headRow.appendChild(th);
    ths.set(col, th);
  }

  filterInput.addEventListener("input", () => {
    filterText = filterInput.value.trim().toLowerCase();
    expandedRowIndex = null;
    renderRows();
  });

  function matchesFilter(row: Record<string, unknown>): boolean {
    if (!filterText) return true;
    return columns.some((col) => formatScalar(cellValue(row, col)).toLowerCase().includes(filterText));
  }

  function sortedFilteredRows(): { row: Record<string, unknown>; index: number }[] {
    const indexed = objRows.map((row, index) => ({ row, index }));
    const filtered = indexed.filter(({ row }) => matchesFilter(row));
    if (!sortBy) return filtered;
    const col = sortBy;
    const dir = sortDir === "desc" ? -1 : 1;
    return [...filtered].sort((a, b) => {
      const av = cellValue(a.row, col);
      const bv = cellValue(b.row, col);
      if (av === bv) return 0;
      if (av === null || av === undefined) return 1;
      if (bv === null || bv === undefined) return -1;
      if (typeof av === "number" && typeof bv === "number") return (av - bv) * dir;
      return formatScalar(av).localeCompare(formatScalar(bv)) * dir;
    });
  }

  function renderRows(): void {
    for (const [col, th] of ths) {
      const arrow = th.querySelector(".lc-sort-arrow")!;
      arrow.textContent = sortBy === col ? (sortDir === "asc" ? "▲" : "▼") : "";
    }

    const visible = sortedFilteredRows();
    count.textContent = `${visible.length} of ${objRows.length} row${objRows.length === 1 ? "" : "s"}`;

    tbody.innerHTML = "";
    for (const { row, index } of visible) {
      const tr = document.createElement("tr");
      tr.dataset.rowIndex = String(index);
      const drilldown = hints?.drilldown;
      const rowId = row["id"];
      const canDrilldown = Boolean(drilldown) && rowId !== undefined && rowId !== null;
      if (canDrilldown) {
        tr.classList.add("lc-row-clickable");
      }
      if (expandedRowIndex === index) {
        tr.classList.add("lc-row-expanded");
      }

      for (const col of columns) {
        const td = document.createElement("td");
        const value = cellValue(row, col);
        if (isBadgeColumn(col, hints) || typeof value === "boolean") {
          td.appendChild(renderPill(Boolean(value)));
        } else {
          td.textContent = formatScalar(value);
          if (isIdLikeColumn(col)) {
            td.classList.add("lc-mono");
          }
        }
        tr.appendChild(td);
      }

      if (canDrilldown) {
        tr.addEventListener("click", () => {
          expandedRowIndex = expandedRowIndex === index ? null : index;
          // renderRows() rebuilds the whole tbody (tbody.innerHTML = ""), which
          // detaches this closure's `tr` from the document — `after()` on a
          // detached node is a silent no-op. Re-query the freshly rendered row
          // by its stable data-row-index instead of reusing the stale `tr`.
          renderRows();
          if (expandedRowIndex === index) {
            const freshRow = tbody.querySelector<HTMLTableRowElement>(
              `tr[data-row-index="${index}"]`,
            );
            if (freshRow) {
              void loadDrilldown(freshRow, columns.length, drilldown!, rowId);
            }
          }
        });
      }

      tbody.appendChild(tr);
    }
  }

  async function loadDrilldown(
    afterRow: HTMLTableRowElement,
    colSpan: number,
    toolName: string,
    id: unknown,
  ): Promise<void> {
    const detailRow = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = colSpan;
    cell.className = "lc-detail-cell";
    const loading = document.createElement("div");
    loading.className = "lc-detail-loading";
    loading.textContent = "Loading…";
    cell.appendChild(loading);
    detailRow.appendChild(cell);
    afterRow.after(detailRow);

    try {
      const outcome = await host.callTool(toolName, { id });
      cell.innerHTML = "";
      if (!outcome.ok) {
        const err = document.createElement("div");
        err.className = "lc-detail-loading";
        err.textContent = outcome.errorMessage ?? "Drilldown failed.";
        cell.appendChild(err);
        return;
      }
      const detail = isPlainObject(outcome.value) ? renderRecord(outcome.value) : renderTree(outcome.value);
      cell.appendChild(detail);
    } catch (err) {
      cell.innerHTML = "";
      const errEl = document.createElement("div");
      errEl.className = "lc-detail-loading";
      errEl.textContent = err instanceof Error ? err.message : String(err);
      cell.appendChild(errEl);
    }
  }

  renderRows();
  return root;
}

function renderPill(value: boolean): HTMLElement {
  const span = document.createElement("span");
  span.className = `lc-pill ${value ? "lc-pill-true" : "lc-pill-false"}`;
  span.textContent = value ? "true" : "false";
  return span;
}

// ---------------------------------------------------------------------------
// Record view
// ---------------------------------------------------------------------------

function renderRecord(obj: Record<string, unknown>): HTMLElement {
  const keys = Object.keys(obj);
  if (keys.length === 0) {
    return renderEmptyState("No data.");
  }

  const table = document.createElement("table");
  table.className = "lc-record";
  const tbody = document.createElement("tbody");
  table.appendChild(tbody);

  for (const key of keys) {
    const value = obj[key];
    const tr = document.createElement("tr");
    const th = document.createElement("th");
    th.textContent = key;
    const td = document.createElement("td");

    if (typeof value === "boolean") {
      td.appendChild(renderPill(value));
    } else if (isPlainObject(value) || Array.isArray(value)) {
      td.appendChild(renderTree(value));
    } else if (isIdLikeColumn(key)) {
      td.classList.add("lc-mono");
      td.textContent = formatScalar(value);
    } else {
      td.textContent = formatScalar(value);
    }

    tr.appendChild(th);
    tr.appendChild(td);
    tbody.appendChild(tr);
  }

  return table;
}

// ---------------------------------------------------------------------------
// Tree / JSON view
// ---------------------------------------------------------------------------

function renderTree(value: unknown): HTMLElement {
  const wrap = document.createElement("div");
  wrap.className = "lc-tree-wrap";

  const copyBtn = document.createElement("button");
  copyBtn.type = "button";
  copyBtn.className = "lc-copy-btn";
  copyBtn.textContent = "Copy JSON";
  copyBtn.addEventListener("click", () => {
    void copyToClipboard(value, copyBtn);
  });
  wrap.appendChild(copyBtn);

  const pre = document.createElement("div");
  pre.className = "lc-json";
  pre.appendChild(buildJsonNode(value, 0));
  wrap.appendChild(pre);

  return wrap;
}

async function copyToClipboard(value: unknown, btn: HTMLButtonElement): Promise<void> {
  const text = (() => {
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return String(value);
    }
  })();

  const original = btn.textContent;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      btn.textContent = "Copied!";
    } else {
      btn.textContent = "Copy unsupported";
    }
  } catch {
    btn.textContent = "Copy failed";
  } finally {
    setTimeout(() => {
      btn.textContent = original;
    }, 1500);
  }
}

function buildJsonNode(value: unknown, depth: number): HTMLElement {
  if (Array.isArray(value)) {
    if (value.length === 0) {
      const span = document.createElement("span");
      span.textContent = "[]";
      return span;
    }
    const details = document.createElement("details");
    details.open = depth < 2;
    const summary = document.createElement("summary");
    summary.textContent = `Array(${value.length})`;
    details.appendChild(summary);
    value.forEach((item, i) => {
      const row = document.createElement("div");
      const key = document.createElement("span");
      key.className = "lc-json-key";
      key.textContent = `${i}: `;
      row.appendChild(key);
      row.appendChild(buildJsonNode(item, depth + 1));
      details.appendChild(row);
    });
    return details;
  }

  if (isPlainObject(value)) {
    const keys = Object.keys(value);
    if (keys.length === 0) {
      const span = document.createElement("span");
      span.textContent = "{}";
      return span;
    }
    const details = document.createElement("details");
    details.open = depth < 2;
    const summary = document.createElement("summary");
    summary.textContent = `Object(${keys.length})`;
    details.appendChild(summary);
    for (const key of keys) {
      const row = document.createElement("div");
      const keyEl = document.createElement("span");
      keyEl.className = "lc-json-key";
      keyEl.textContent = `${key}: `;
      row.appendChild(keyEl);
      row.appendChild(buildJsonNode(value[key], depth + 1));
      details.appendChild(row);
    }
    return details;
  }

  const span = document.createElement("span");
  if (value === null || value === undefined) {
    span.className = "lc-json-null";
    span.textContent = "null";
  } else if (typeof value === "string") {
    span.className = "lc-json-string";
    span.textContent = JSON.stringify(value);
  } else if (typeof value === "number") {
    span.className = "lc-json-num";
    span.textContent = String(value);
  } else if (typeof value === "boolean") {
    span.className = "lc-json-bool";
    span.textContent = String(value);
  } else {
    span.textContent = formatScalar(value);
  }
  return span;
}
