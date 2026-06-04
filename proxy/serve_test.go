package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talosred/ce/metrics"
	"github.com/talosred/ce/store"
)

// waitForLogs polls the store until n rows are present or the budget runs out.
func waitForLogs(t *testing.T, s *store.Store, n int) []*store.RequestLog {
	t.Helper()
	for range 50 {
		logs, _ := s.List(store.ListFilter{})
		if len(logs) >= n {
			return logs
		}
		time.Sleep(20 * time.Millisecond)
	}
	logs, _ := s.List(store.ListFilter{})
	return logs
}

// newTestStore builds an in-memory store + cost calculator for handler tests.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.New(db, nil)
}

// fakeOpenAI returns a server that answers /v1/chat/completions with a fixed body.
func fakeOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion", "model": "gpt-4o",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": "hi"},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
}

func doChat(t *testing.T, h *Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServeHTTPWrongPath(t *testing.T) {
	h := NewHandler(newTestStore(t), metrics.NewCostCalculator(newTestStore(t)), nil, "")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d want 404", rec.Code)
	}
}

func TestServeHTTPInvalidJSON(t *testing.T) {
	s := newTestStore(t)
	h := NewHandler(s, metrics.NewCostCalculator(s), nil, "")
	rec := doChat(t, h, "{not json", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400", rec.Code)
	}
}

func TestServeHTTPProxyKeyRejected(t *testing.T) {
	s := newTestStore(t)
	h := NewHandler(s, metrics.NewCostCalculator(s), nil, "sk-talos-local")
	rec := doChat(t, h, `{"model":"gpt-4o","messages":[]}`, map[string]string{
		"Authorization": "Bearer WRONG",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d want 401", rec.Code)
	}
}

func TestServeHTTPEndToEndWithAttribution(t *testing.T) {
	upstream := fakeOpenAI(t)
	defer upstream.Close()

	s := newTestStore(t)
	// alias gpt-4o -> the fake upstream so no real network call
	if err := s.UpsertAlias(&store.ModelAlias{
		Pattern: "gpt-4o", TargetModel: "gpt-4o", TargetURL: upstream.URL,
		Provider: "openai", Enabled: true,
	}); err != nil {
		t.Fatalf("alias: %v", err)
	}
	t.Setenv("OPENAI_API_KEY", "sk-test")

	h := NewHandler(s, metrics.NewCostCalculator(s), nil, "")
	rec := doChat(t, h, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		map[string]string{"X-Talos-App": "billing", "X-Talos-User": "dave"})

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200, body=%s", rec.Code, rec.Body.String())
	}

	// logging is async (go h.logRequest); poll briefly
	logs := waitForLogs(t, s, 1)
	if len(logs) != 1 {
		t.Fatalf("expected 1 logged request, got %d", len(logs))
	}
	got := logs[0]
	if got.AppName != "billing" || got.UserName != "dave" {
		t.Errorf("attribution not captured: app=%q user=%q", got.AppName, got.UserName)
	}
	if got.StatusCode != 200 {
		t.Errorf("status: got %d want 200", got.StatusCode)
	}
	if got.InputTok != 10 || got.OutputTok != 5 {
		t.Errorf("tokens: in=%d out=%d want 10/5", got.InputTok, got.OutputTok)
	}
	if got.UpstreamURL == "" || got.UpstreamBody == "" {
		t.Error("upstream capture missing")
	}
	// real key must not be persisted in headers
	if strings.Contains(got.UpstreamHeaders, "sk-test") {
		t.Errorf("upstream headers leaked key: %s", got.UpstreamHeaders)
	}
}

func TestServeHTTPUpstreamErrorLogged(t *testing.T) {
	// upstream returns 400 — should be logged and passed through
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad"}`))
	}))
	defer upstream.Close()

	s := newTestStore(t)
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-4o", TargetModel: "gpt-4o", TargetURL: upstream.URL, Provider: "openai", Enabled: true})
	t.Setenv("OPENAI_API_KEY", "sk-test")

	h := NewHandler(s, metrics.NewCostCalculator(s), nil, "")
	rec := doChat(t, h, `{"model":"gpt-4o","messages":[]}`, nil)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d want 400 passthrough", rec.Code)
	}

	logs := waitForLogs(t, s, 1)
	if len(logs) != 1 || logs[0].StatusCode != 400 {
		t.Errorf("failed request not logged with status 400: %+v", logs)
	}
}

func TestServeHTTPMissingUpstreamKey(t *testing.T) {
	// no alias, no OPENAI_API_KEY -> BuildRequest fails -> 500
	s := newTestStore(t)
	t.Setenv("OPENAI_API_KEY", "")
	h := NewHandler(s, metrics.NewCostCalculator(s), nil, "")
	rec := doChat(t, h, `{"model":"gpt-4o","messages":[]}`, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("got %d want 500", rec.Code)
	}
}

func TestServeHTTPStreaming(t *testing.T) {
	// fake streaming upstream emitting OpenAI SSE chunks
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"id":"c","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			`data: {"id":"c","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte(c + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer upstream.Close()

	s := newTestStore(t)
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-4o", TargetModel: "gpt-4o", TargetURL: upstream.URL, Provider: "openai", Enabled: true})
	t.Setenv("OPENAI_API_KEY", "sk-test")

	h := NewHandler(s, metrics.NewCostCalculator(s), nil, "")
	proxy := httptest.NewServer(h)
	defer proxy.Close()

	resp, err := http.Post(proxy.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type: %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "data:") || !strings.Contains(string(body), "[DONE]") {
		t.Errorf("stream body missing SSE markers: %s", body)
	}

	logs := waitForLogs(t, s, 1)
	if len(logs) != 1 {
		t.Fatalf("expected 1 logged stream request, got %d", len(logs))
	}
	if logs[0].StatusCode != 200 {
		t.Errorf("stream status: got %d", logs[0].StatusCode)
	}
}
