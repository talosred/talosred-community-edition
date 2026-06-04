package proxy

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func readBody(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// ---- OpenAI passthrough ----------------------------------------------------

func TestOpenAIBuildRequest(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	tr := &OpenAIPassthrough{}
	req, err := tr.BuildRequest(&ChatRequest{Model: "gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.URL.String() != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("url: %s", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("auth: %q", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type: %q", got)
	}
}

func TestOpenAIBuildRequestMissingKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	tr := &OpenAIPassthrough{}
	if _, err := tr.BuildRequest(&ChatRequest{Model: "gpt-4o"}); err == nil {
		t.Error("expected error when OPENAI_API_KEY unset and no BaseURL")
	}
}

func TestOpenAIBuildRequestLocalNoKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	tr := &OpenAIPassthrough{BaseURL: "http://localhost:11434"}
	req, err := tr.BuildRequest(&ChatRequest{Model: "llama3.2"})
	if err != nil {
		t.Fatalf("local build should not need key: %v", err)
	}
	if req.URL.String() != "http://localhost:11434/v1/chat/completions" {
		t.Errorf("url: %s", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "Bearer local" {
		t.Errorf("expected dummy local token, got %q", got)
	}
}

// ---- Anthropic -------------------------------------------------------------

func TestAnthropicBuildRequest(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	tr := &AnthropicTranslator{}
	temp := 0.5
	req, err := tr.BuildRequest(&ChatRequest{
		Model:       "claude-3-5-sonnet-20241022",
		MaxTokens:   256,
		Temperature: &temp,
		Messages: []Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.URL.String() != "https://api.anthropic.com/v1/messages" {
		t.Errorf("url: %s", req.URL.String())
	}
	if got := req.Header.Get("x-api-key"); got != "sk-ant" {
		t.Errorf("x-api-key: %q", got)
	}
	if req.Header.Get("anthropic-version") == "" {
		t.Error("missing anthropic-version header")
	}

	var sent map[string]any
	if err := json.Unmarshal(readBody(t, req.Body), &sent); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// system message must be hoisted to top-level "system", not in messages
	if sent["system"] != "be brief" {
		t.Errorf("system not hoisted: %v", sent["system"])
	}
	msgs, _ := sent["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("expected 1 non-system message, got %d", len(msgs))
	}
	if sent["max_tokens"].(float64) != 256 {
		t.Errorf("max_tokens: %v", sent["max_tokens"])
	}
}

func TestAnthropicBuildRequestDefaultMaxTokens(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant")
	tr := &AnthropicTranslator{}
	req, err := tr.BuildRequest(&ChatRequest{Model: "claude-3-5-sonnet-20241022", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var sent map[string]any
	json.Unmarshal(readBody(t, req.Body), &sent)
	if sent["max_tokens"].(float64) == 0 {
		t.Error("expected a non-zero default max_tokens")
	}
}

func TestAnthropicBuildRequestMissingKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	tr := &AnthropicTranslator{}
	if _, err := tr.BuildRequest(&ChatRequest{Model: "claude-3-5-sonnet-20241022"}); err == nil {
		t.Error("expected error when ANTHROPIC_API_KEY unset")
	}
}

// ---- Gemini ----------------------------------------------------------------

func TestGeminiBuildRequest(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "g-key")
	tr := &GeminiTranslator{}
	req, err := tr.BuildRequest(&ChatRequest{
		Model: "gemini-1.5-pro",
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "yo"},
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(req.URL.Path, "gemini-1.5-pro:generateContent") {
		t.Errorf("url path: %s", req.URL.Path)
	}
	if req.URL.Query().Get("key") != "g-key" {
		t.Errorf("key query: %s", req.URL.Query().Get("key"))
	}

	var sent map[string]any
	if err := json.Unmarshal(readBody(t, req.Body), &sent); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if sent["systemInstruction"] == nil {
		t.Error("system not mapped to systemInstruction")
	}
	contents, _ := sent["contents"].([]any)
	if len(contents) != 2 {
		t.Fatalf("expected 2 contents (user, assistant), got %d", len(contents))
	}
	// assistant role must be mapped to "model"
	last := contents[1].(map[string]any)
	if last["role"] != "model" {
		t.Errorf("assistant role not mapped to model: %v", last["role"])
	}
}

func TestGeminiBuildRequestStreamURL(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "g-key")
	tr := &GeminiTranslator{}
	req, err := tr.BuildRequest(&ChatRequest{Model: "gemini-1.5-pro", Stream: true, Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(req.URL.Path, "streamGenerateContent") {
		t.Errorf("expected streamGenerateContent, got %s", req.URL.Path)
	}
	if req.URL.Query().Get("alt") != "sse" {
		t.Errorf("expected alt=sse, got %q", req.URL.Query().Get("alt"))
	}
}

func TestGeminiBuildRequestGoogleKeyFallback(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "goog")
	tr := &GeminiTranslator{}
	req, err := tr.BuildRequest(&ChatRequest{Model: "gemini-1.5-pro"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if req.URL.Query().Get("key") != "goog" {
		t.Errorf("expected GOOGLE_API_KEY fallback, got %q", req.URL.Query().Get("key"))
	}
}

func TestGeminiBuildRequestMissingKey(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	tr := &GeminiTranslator{}
	if _, err := tr.BuildRequest(&ChatRequest{Model: "gemini-1.5-pro"}); err == nil {
		t.Error("expected error when no Gemini key set")
	}
}
