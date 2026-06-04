package dashboard_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/talosred/ce/dashboard"
	"github.com/talosred/ce/hooks"
	"github.com/talosred/ce/store"
)

func newTestServer(t *testing.T) (*dashboard.Server, *store.Store) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := store.New(db, store.NewBroadcaster())
	hr := hooks.NewRunner(t.TempDir(), 0)
	srv := dashboard.NewServer(s, store.NewBroadcaster(), nil, hr)
	return srv, s
}

func get(t *testing.T, srv *dashboard.Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestPagesRender(t *testing.T) {
	srv, _ := newTestServer(t)
	cases := []struct {
		path, contains string
	}{
		{"/ui", "Request Logs"},
		{"/ui/usage", "Usage &amp; Attribution"},
		{"/ui/aliases", "Model Aliases"},
		{"/ui/pricing", "Model Pricing"},
		{"/ui/hooks", "Hooks"},
		{"/ui/settings", "API Keys"},
	}
	for _, c := range cases {
		rec := get(t, srv, c.path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: got %d want 200", c.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), c.contains) {
			t.Errorf("%s: body missing %q", c.path, c.contains)
		}
	}
}

func TestPricingSeededInPage(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/ui/pricing")
	if !strings.Contains(rec.Body.String(), "gpt-4o") {
		t.Error("pricing page missing seeded gpt-4o")
	}
}

func TestLogsFilterByApp(t *testing.T) {
	srv, s := newTestServer(t)
	s.Insert(&store.RequestLog{ID: "a", TS: time.Now(), Provider: "openai", Model: "gpt-4o", AppName: "billing"})
	s.Insert(&store.RequestLog{ID: "b", TS: time.Now(), Provider: "openai", Model: "gpt-4o", AppName: "search"})

	rec := get(t, srv, "/ui/requests?app=billing")
	body := rec.Body.String()
	if !strings.Contains(body, "billing") {
		t.Error("filtered list should include billing row")
	}
	if strings.Contains(body, "search") {
		t.Error("filtered list should exclude search row")
	}
}

func TestRequestDetailHasCurl(t *testing.T) {
	srv, s := newTestServer(t)
	s.Insert(&store.RequestLog{
		ID: "det-1", TS: time.Now(), Provider: "openai", Model: "gpt-4o",
		StatusCode: 200, AppName: "billing",
		UpstreamURL:     "https://api.openai.com/v1/chat/completions",
		UpstreamHeaders: `{"Authorization":"REDACTED","Content-Type":"application/json"}`,
		UpstreamBody:    `{"model":"gpt-4o"}`,
	})

	rec := get(t, srv, "/ui/requests/det-1")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	for _, want := range []string{"Copy as cURL", "curl -X POST", "REDACTED", "api.openai.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

func TestRequestDetailNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/ui/requests/nope")
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d want 404", rec.Code)
	}
}

func TestAliasCRUDViaHTTP(t *testing.T) {
	srv, s := newTestServer(t)

	// create
	form := url.Values{
		"pattern":      {"gpt-3.5-turbo"},
		"target_model": {"llama3.2"},
		"target_url":   {"http://localhost:11434"},
		"provider":     {"openai"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ui/aliases", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create alias: got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "llama3.2") {
		t.Error("response should list the new alias")
	}

	// toggle
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/aliases/gpt-3.5-turbo/toggle", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle: got %d", rec.Code)
	}
	a, _ := s.GetAlias("gpt-3.5-turbo")
	if a == nil || a.Enabled {
		t.Error("toggle should have disabled the alias")
	}

	// delete
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/ui/aliases/gpt-3.5-turbo", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d", rec.Code)
	}
	if a, _ := s.GetAlias("gpt-3.5-turbo"); a != nil {
		t.Error("alias should be deleted")
	}
}

func TestPricingUpsertViaHTTP(t *testing.T) {
	srv, s := newTestServer(t)
	form := url.Values{
		"model":         {"custom-model"},
		"provider":      {"openai"},
		"input_per_1k":  {"0.001"},
		"output_per_1k": {"0.002"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ui/pricing", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert pricing: got %d", rec.Code)
	}
	p, _ := s.GetPricing("custom-model")
	if p == nil || p.InputPer1k != 0.001 {
		t.Errorf("pricing not persisted: %+v", p)
	}
}

func TestUsageAggregatesByApp(t *testing.T) {
	srv, s := newTestServer(t)
	s.Insert(&store.RequestLog{ID: "1", TS: time.Now(), Provider: "openai", Model: "gpt-4o", AppName: "billing", CostUSD: 0.05})
	rec := get(t, srv, "/ui/usage")
	if !strings.Contains(rec.Body.String(), "billing") {
		t.Error("usage page should list billing app")
	}
}

func TestStaticAssetServed(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := get(t, srv, "/static/style.css")
	if rec.Code != http.StatusOK {
		t.Errorf("static css: got %d want 200", rec.Code)
	}
}

func TestAliasUpsertMissingFields(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ui/aliases", strings.NewReader("pattern=&target_model="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400 for missing fields", rec.Code)
	}
}

func TestPricingItemEditCancelDelete(t *testing.T) {
	srv, s := newTestServer(t)

	// edit form for a seeded model
	rec := get(t, srv, "/ui/pricing/gpt-4o/edit")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gpt-4o") {
		t.Errorf("edit row: code=%d", rec.Code)
	}

	// cancel returns the read-only row
	rec = get(t, srv, "/ui/pricing/gpt-4o/cancel")
	if rec.Code != http.StatusOK {
		t.Errorf("cancel row: code=%d", rec.Code)
	}

	// delete
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/ui/pricing/gpt-4o", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("delete: code=%d", rec.Code)
	}
	if p, _ := s.GetPricing("gpt-4o"); p != nil {
		t.Error("gpt-4o pricing should be deleted")
	}
}

func TestHookToggleViaHTTP(t *testing.T) {
	// server needs a hooks dir containing one executable hook
	db, _ := store.Open(":memory:")
	t.Cleanup(func() { db.Close() })
	store.Migrate(db)
	s := store.New(db, store.NewBroadcaster())

	dir := t.TempDir()
	preDir := filepath.Join(dir, "pre-request")
	os.MkdirAll(preDir, 0o755)
	os.WriteFile(filepath.Join(preDir, "01-x.sh"), []byte("#!/bin/sh\necho '{\"action\":\"proceed\"}'"), 0o755)

	hr := hooks.NewRunner(dir, 0)
	srv := dashboard.NewServer(s, store.NewBroadcaster(), nil, hr)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/hooks/pre-request/01-x.sh/toggle", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("toggle: got %d", rec.Code)
	}
	// after toggle the hook should be disabled
	for _, h := range hr.ListHooks() {
		if h.Name == "01-x.sh" && h.Enabled {
			t.Error("hook should be disabled after toggle")
		}
	}
}

func TestSSEStreamEmitsRow(t *testing.T) {
	db, _ := store.Open(":memory:")
	t.Cleanup(func() { db.Close() })
	store.Migrate(db)
	b := store.NewBroadcaster()
	s := store.New(db, b)
	srv := dashboard.NewServer(s, b, nil, hooks.NewRunner(t.TempDir(), 0))

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/ui/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect sse: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type: %q", ct)
	}

	// publish a row; the SSE handler should render and push it
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.Insert(&store.RequestLog{ID: "sse-1", TS: time.Now(), Provider: "openai", Model: "gpt-4o"})
	}()

	buf := make([]byte, 4096)
	deadline := time.Now().Add(1500 * time.Millisecond)
	var got string
	for time.Now().Before(deadline) {
		n, _ := resp.Body.Read(buf)
		got += string(buf[:n])
		if strings.Contains(got, "gpt-4o") {
			break
		}
	}
	if !strings.Contains(got, "gpt-4o") {
		t.Errorf("SSE did not emit the new row; got: %q", got)
	}
}
