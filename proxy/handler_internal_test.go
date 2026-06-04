package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExtractBearer(t *testing.T) {
	cases := map[string]string{
		"Bearer sk-talos-local": "sk-talos-local",
		"sk-raw-token":          "sk-raw-token",
		"Bearer  spaced ":       "spaced",
		"":                      "",
	}
	for in, want := range cases {
		if got := extractBearer(in); got != want {
			t.Errorf("extractBearer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactURL(t *testing.T) {
	u, _ := url.Parse("https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-pro:generateContent?key=SECRET&alt=sse")
	got := redactURL(u)
	if strings.Contains(got, "SECRET") {
		t.Errorf("redactURL leaked key: %s", got)
	}
	if !strings.Contains(got, "key=REDACTED") {
		t.Errorf("redactURL did not redact: %s", got)
	}

	plain, _ := url.Parse("https://api.openai.com/v1/chat/completions")
	if got := redactURL(plain); got != plain.String() {
		t.Errorf("redactURL altered keyless url: %s", got)
	}
}

func TestRedactHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("X-Api-Key", "anthropic-secret")
	h.Set("Content-Type", "application/json")

	got := redactHeaders(h)
	if strings.Contains(got, "sk-secret") || strings.Contains(got, "anthropic-secret") {
		t.Errorf("redactHeaders leaked secret: %s", got)
	}
	if !strings.Contains(got, "application/json") {
		t.Errorf("redactHeaders dropped safe header: %s", got)
	}
}

func TestRetryWait(t *testing.T) {
	// Retry-After header honoured
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "5")
	if got := retryWait(resp, 0); got != 5*time.Second {
		t.Errorf("Retry-After: got %v want 5s", got)
	}

	// Retry-After capped
	resp.Header.Set("Retry-After", "9999")
	if got := retryWait(resp, 0); got != maxRetryWait {
		t.Errorf("Retry-After cap: got %v want %v", got, maxRetryWait)
	}

	// no header → linear backoff
	bare := &http.Response{Header: http.Header{}}
	if got := retryWait(bare, 0); got != baseRetryWait {
		t.Errorf("backoff attempt 0: got %v want %v", got, baseRetryWait)
	}
	if got := retryWait(bare, 1); got != 2*baseRetryWait {
		t.Errorf("backoff attempt 1: got %v want %v", got, 2*baseRetryWait)
	}
}

func TestDoUpstreamRetriesOn429(t *testing.T) {
	// shrink backoff so the test is fast
	orig := baseRetryWait
	baseRetryWait = time.Millisecond
	defer func() { baseRetryWait = orig }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","choices":[]}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test")
	h := &Handler{client: srv.Client()}
	tr := &OpenAIPassthrough{BaseURL: srv.URL}

	resp, _, retries, err := h.doUpstream(context.Background(), tr, &ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("doUpstream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if retries != 2 {
		t.Errorf("retries: got %d want 2", retries)
	}
	if calls != 3 {
		t.Errorf("upstream calls: got %d want 3", calls)
	}
}

func TestDoUpstreamGivesUpAfterMaxAttempts(t *testing.T) {
	orig := baseRetryWait
	baseRetryWait = time.Millisecond
	defer func() { baseRetryWait = orig }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test")
	h := &Handler{client: srv.Client()}
	tr := &OpenAIPassthrough{BaseURL: srv.URL}

	resp, _, retries, err := h.doUpstream(context.Background(), tr, &ChatRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("doUpstream: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status: got %d want 429", resp.StatusCode)
	}
	if calls != maxAttempts {
		t.Errorf("calls: got %d want %d", calls, maxAttempts)
	}
	if retries != maxAttempts-1 {
		t.Errorf("retries: got %d want %d", retries, maxAttempts-1)
	}
}

func TestCaptureUpstreamMetaRestoresBody(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	tr := &OpenAIPassthrough{}
	req, err := tr.BuildRequest(&ChatRequest{Model: "gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	meta := captureUpstreamMeta(req)
	if meta.body == "" {
		t.Error("expected captured body")
	}
	if !strings.Contains(meta.body, "gpt-4o") {
		t.Errorf("body missing model: %s", meta.body)
	}
	// secret must be redacted in headers
	if strings.Contains(meta.headers, "test") && strings.Contains(meta.headers, "Bearer test") {
		t.Errorf("headers leaked key: %s", meta.headers)
	}

	// body must still be readable for the real send
	sent, _ := readAll(req)
	if string(sent) != meta.body {
		t.Errorf("body not restored: sent %q meta %q", sent, meta.body)
	}
}

func readAll(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer req.Body.Close()
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	for {
		n, err := req.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}
