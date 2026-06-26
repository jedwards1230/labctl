package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/jedwards1230/labctl/internal/command"
	"github.com/jedwards1230/labctl/internal/manifest"
)

// pageNumberService builds a service whose `list` command paginates by page
// number with the given style ("page-number" or "page-until-short").
func pageNumberService(baseURL, style string) *manifest.Service {
	return &manifest.Service{
		Name:    "paged",
		BaseURL: baseURL,
		Auth:    manifest.Auth{Strategy: "none"},
		Pagination: manifest.Pagination{
			Style: style,
			Param: "page",
			Data:  ".records",
		},
		Commands: map[string]manifest.Command{
			"list": {Method: "GET", Path: "/api/list"},
		},
	}
}

// runPaged executes the `list` command and returns the accumulated .records
// item count plus the number of HTTP calls observed.
func runPaged(t *testing.T, svc *manifest.Service, limit int) []any {
	t.Helper()
	cmds := command.FromManifest(svc)
	res, err := Execute(t.Context(), Request{
		Config:  manifest.Config{},
		Service: svc,
		Command: cmds["list"],
		Flags:   Flags{Limit: limit},
		Runner:  fakeOp,
		Getenv:  func(string) string { return "" },
	}, nil)
	if err != nil {
		t.Fatalf("execute paged: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body, &body); err != nil {
		t.Fatalf("parse synthesized body: %v (%s)", err, res.Body)
	}
	items, ok := body["records"].([]any)
	if !ok {
		t.Fatalf("synthesized body missing []records: %s", res.Body)
	}
	return items
}

// TestPageNumberPagination drives page-number style: pages of 3, 3, then a
// short page of 2 signals the end. The engine must accumulate all 8 items and
// stop after the short page (4 requests: pages 1-3 full-ish + the short one).
func TestPageNumberPagination(t *testing.T) {
	// page 1 → 3 items, page 2 → 3 items, page 3 → 2 items (short → stop).
	pages := map[string]string{
		"1": `{"records":[{"id":1},{"id":2},{"id":3}]}`,
		"2": `{"records":[{"id":4},{"id":5},{"id":6}]}`,
		"3": `{"records":[{"id":7},{"id":8}]}`,
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := r.URL.Query().Get("page")
		body, ok := pages[page]
		if !ok {
			// Beyond the known pages return empty — should never be reached
			// because the short page stops accumulation first.
			t.Errorf("unexpected request for page %q", page)
			body = `{"records":[]}`
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	items := runPaged(t, pageNumberService(srv.URL, "page-number"), 0)
	if len(items) != 8 {
		t.Fatalf("want 8 items across pages, got %d", len(items))
	}
	if calls != 3 {
		t.Fatalf("want 3 HTTP calls (stop on short page 3), got %d", calls)
	}
	assertSequentialIDs(t, items, 8)
}

// TestPageNumberEmptyPageStops verifies the empty-page stop condition: equal-size
// full pages followed by an empty page terminates the loop.
func TestPageNumberEmptyPageStops(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		switch page {
		case 1:
			_, _ = w.Write([]byte(`{"records":[{"id":1},{"id":2}]}`))
		case 2:
			_, _ = w.Write([]byte(`{"records":[{"id":3},{"id":4}]}`))
		default:
			_, _ = w.Write([]byte(`{"records":[]}`))
		}
	}))
	defer srv.Close()

	items := runPaged(t, pageNumberService(srv.URL, "page-number"), 0)
	if len(items) != 4 {
		t.Fatalf("want 4 items, got %d", len(items))
	}
	// Page 1 (2), page 2 (2, same size → continue), page 3 (empty → stop): 3 calls.
	if calls != 3 {
		t.Fatalf("want 3 HTTP calls (empty page stops), got %d", calls)
	}
}

// TestPageNumberLimitTruncates verifies --limit truncates accumulation and stops
// requesting further pages.
func TestPageNumberLimitTruncates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Every page returns 3 items; limit=4 should stop after page 2.
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		base := (page - 1) * 3
		_, _ = w.Write([]byte(`{"records":[{"id":` +
			strconv.Itoa(base+1) + `},{"id":` +
			strconv.Itoa(base+2) + `},{"id":` +
			strconv.Itoa(base+3) + `}]}`))
	}))
	defer srv.Close()

	items := runPaged(t, pageNumberService(srv.URL, "page-number"), 4)
	if len(items) != 4 {
		t.Fatalf("want 4 items (limit), got %d", len(items))
	}
	if calls != 2 {
		t.Fatalf("want 2 HTTP calls (limit reached on page 2), got %d", calls)
	}
}

// TestPageUntilShortPagination drives page-until-short: the first page's length
// is the reference; the first strictly-shorter page is the last. Pages of 4, 4,
// 1 → 9 items, 3 calls.
func TestPageUntilShortPagination(t *testing.T) {
	pages := map[string]string{
		"1": `{"records":[{"id":1},{"id":2},{"id":3},{"id":4}]}`,
		"2": `{"records":[{"id":5},{"id":6},{"id":7},{"id":8}]}`,
		"3": `{"records":[{"id":9}]}`,
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := r.URL.Query().Get("page")
		body, ok := pages[page]
		if !ok {
			t.Errorf("unexpected request for page %q", page)
			body = `{"records":[]}`
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	items := runPaged(t, pageNumberService(srv.URL, "page-until-short"), 0)
	if len(items) != 9 {
		t.Fatalf("want 9 items across pages, got %d", len(items))
	}
	if calls != 3 {
		t.Fatalf("want 3 HTTP calls (short page 3 stops), got %d", calls)
	}
	assertSequentialIDs(t, items, 9)
}

// TestPageUntilShortLimitTruncates verifies --limit truncation for the
// page-until-short style.
func TestPageUntilShortLimitTruncates(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		base := (page - 1) * 2
		_, _ = w.Write([]byte(`{"records":[{"id":` +
			strconv.Itoa(base+1) + `},{"id":` +
			strconv.Itoa(base+2) + `}]}`))
	}))
	defer srv.Close()

	items := runPaged(t, pageNumberService(srv.URL, "page-until-short"), 3)
	if len(items) != 3 {
		t.Fatalf("want 3 items (limit), got %d", len(items))
	}
	// page 1 (2 items), page 2 (2 items → total 4 ≥ 3 → truncate to 3): 2 calls.
	if calls != 2 {
		t.Fatalf("want 2 HTTP calls (limit reached on page 2), got %d", calls)
	}
}

// assertSequentialIDs asserts items hold id=1..n in order.
func assertSequentialIDs(t *testing.T, items []any, n int) {
	t.Helper()
	if len(items) != n {
		t.Fatalf("want %d items, got %d", n, len(items))
	}
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("item %d not an object: %T", i, it)
		}
		id, ok := m["id"].(float64)
		if !ok {
			t.Fatalf("item %d id not a number: %v", i, m["id"])
		}
		if int(id) != i+1 {
			t.Fatalf("item %d: want id=%d, got id=%v", i, i+1, id)
		}
	}
}
