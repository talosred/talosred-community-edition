package proxy_test

import (
	"encoding/json"
	"testing"

	"github.com/talosred/ce/proxy"
)

// ---- DetectProvider --------------------------------------------------------

func TestDetectProvider(t *testing.T) {
	cases := []struct {
		model    string
		expected string
	}{
		{"gpt-4o", "openai"},
		{"gpt-4o-mini", "openai"},
		{"gpt-4-turbo", "openai"},
		{"gpt-3.5-turbo", "openai"},
		{"o1", "openai"},
		{"o1-mini", "openai"},
		{"o3-mini", "openai"},
		{"claude-3-5-sonnet-20241022", "anthropic"},
		{"claude-3-opus-20240229", "anthropic"},
		{"claude-3-haiku-20240307", "anthropic"},
		{"gemini-1.5-pro", "gemini"},
		{"gemini-2.5-flash", "gemini"},
		{"unknown-model", "openai"}, // default
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := proxy.DetectProvider(tc.model)
			if got != tc.expected {
				t.Errorf("DetectProvider(%q) = %q, want %q", tc.model, got, tc.expected)
			}
		})
	}
}

// ---- Anthropic non-streaming -----------------------------------------------

func TestAnthropicTranslateResponseValid(t *testing.T) {
	raw := `{
		"id": "msg_abc123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello"}, {"type": "text", "text": ", world!"}],
		"model": "claude-3-5-sonnet-20241022",
		"usage": {"input_tokens": 20, "output_tokens": 8}
	}`

	tr := proxy.AnthropicTranslator{}
	resp, err := tr.TranslateResponse([]byte(raw), "req-1", "claude-3-5-sonnet-20241022", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hello, world!" {
		t.Errorf("content: got %q want %q", resp.Choices[0].Message.Content, "Hello, world!")
	}
	if resp.Usage.PromptTokens != 20 {
		t.Errorf("input tokens: got %d want 20", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 8 {
		t.Errorf("output tokens: got %d want 8", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 28 {
		t.Errorf("total tokens: got %d want 28", resp.Usage.TotalTokens)
	}
}

func TestAnthropicTranslateResponseInvalid(t *testing.T) {
	tr := proxy.AnthropicTranslator{}
	_, err := tr.TranslateResponse([]byte("not json"), "req-1", "claude", 1000)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ---- Anthropic streaming ---------------------------------------------------

func TestAnthropicStreamMessageStart(t *testing.T) {
	line := `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":25,"output_tokens":1}}}`

	tr := proxy.AnthropicTranslator{}
	chunk, inTok, _, isDone, err := tr.TranslateStreamLine(line, "req-1", "claude-3-5-sonnet-20241022", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDone {
		t.Error("message_start should not signal done")
	}
	if chunk == nil {
		t.Fatal("expected chunk for message_start (role delta)")
	}
	if chunk.Choices[0].Delta.Role != "assistant" {
		t.Errorf("role: got %q want assistant", chunk.Choices[0].Delta.Role)
	}
	if inTok != 25 {
		t.Errorf("input tokens: got %d want 25", inTok)
	}
}

func TestAnthropicStreamContentDelta(t *testing.T) {
	line := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`

	tr := proxy.AnthropicTranslator{}
	chunk, _, _, isDone, err := tr.TranslateStreamLine(line, "req-1", "claude-3-5-sonnet-20241022", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDone {
		t.Error("content_block_delta should not signal done")
	}
	if chunk == nil {
		t.Fatal("expected chunk")
	}
	if chunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("content: got %q want Hello", chunk.Choices[0].Delta.Content)
	}
}

func TestAnthropicStreamMessageDelta(t *testing.T) {
	line := `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`

	tr := proxy.AnthropicTranslator{}
	chunk, _, outTok, isDone, err := tr.TranslateStreamLine(line, "req-1", "claude-3-5-sonnet-20241022", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDone {
		t.Error("message_delta should not signal done (message_stop does)")
	}
	if outTok != 42 {
		t.Errorf("output tokens: got %d want 42", outTok)
	}
	if chunk == nil {
		t.Fatal("expected finish chunk")
	}
	if chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason: got %q want stop", chunk.Choices[0].FinishReason)
	}
}

func TestAnthropicStreamMessageStop(t *testing.T) {
	line := `data: {"type":"message_stop"}`

	tr := proxy.AnthropicTranslator{}
	chunk, _, _, isDone, err := tr.TranslateStreamLine(line, "req-1", "claude-3-5-sonnet-20241022", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDone {
		t.Error("message_stop should signal done")
	}
	if chunk != nil {
		t.Error("message_stop should return nil chunk")
	}
}

func TestAnthropicStreamDoneMarker(t *testing.T) {
	tr := proxy.AnthropicTranslator{}
	_, _, _, isDone, err := tr.TranslateStreamLine("data: [DONE]", "req-1", "claude", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDone {
		t.Error("[DONE] should signal done")
	}
}

func TestAnthropicStreamSkipsNonDataLine(t *testing.T) {
	tr := proxy.AnthropicTranslator{}
	chunk, _, _, isDone, err := tr.TranslateStreamLine("event: ping", "req-1", "claude", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDone || chunk != nil {
		t.Error("non-data line should return nil chunk and not signal done")
	}
}

// ---- Gemini non-streaming --------------------------------------------------

func TestGeminiTranslateResponse(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {"parts": [{"text": "Hi there!"}], "role": "model"},
			"finishReason": "STOP",
			"index": 0
		}],
		"usageMetadata": {
			"promptTokenCount": 15,
			"candidatesTokenCount": 3,
			"totalTokenCount": 18
		}
	}`

	tr := proxy.GeminiTranslator{}
	resp, err := tr.TranslateResponse([]byte(raw), "req-1", "gemini-1.5-pro", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "Hi there!" {
		t.Errorf("content: got %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 15 {
		t.Errorf("prompt tokens: got %d want 15", resp.Usage.PromptTokens)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason: got %q want stop", resp.Choices[0].FinishReason)
	}
}

func TestGeminiTranslateResponseNoCandidates(t *testing.T) {
	raw := `{"candidates":[], "usageMetadata":{}}`

	tr := proxy.GeminiTranslator{}
	_, err := tr.TranslateResponse([]byte(raw), "req-1", "gemini-1.5-pro", 1000)
	if err == nil {
		t.Error("expected error for empty candidates")
	}
}

func TestGeminiTranslateResponseInvalid(t *testing.T) {
	tr := proxy.GeminiTranslator{}
	_, err := tr.TranslateResponse([]byte("not json"), "req-1", "gemini", 1000)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestGeminiTranslateResponseMultiPart(t *testing.T) {
	raw := `{
		"candidates": [{
			"content": {"parts": [{"text": "Part1"}, {"text": "Part2"}], "role": "model"},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 10, "totalTokenCount": 15}
	}`

	tr := proxy.GeminiTranslator{}
	resp, err := tr.TranslateResponse([]byte(raw), "req-1", "gemini-1.5-pro", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "Part1Part2" {
		t.Errorf("content: got %q want Part1Part2", resp.Choices[0].Message.Content)
	}
}

// ---- Gemini streaming ------------------------------------------------------

func TestGeminiStreamChunk(t *testing.T) {
	payload := map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"parts": []any{map[string]any{"text": "Hello"}}, "role": "model"},
			"finishReason": "",
			"index":        0,
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     float64(10),
			"candidatesTokenCount": float64(2),
		},
	}
	data, _ := json.Marshal(payload)
	line := "data: " + string(data)

	tr := proxy.GeminiTranslator{}
	chunk, inTok, outTok, isDone, err := tr.TranslateStreamLine(line, "req-1", "gemini-1.5-pro", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDone {
		t.Error("non-final chunk should not signal done")
	}
	if chunk == nil {
		t.Fatal("expected chunk")
	}
	if chunk.Choices[0].Delta.Content != "Hello" {
		t.Errorf("content: got %q want Hello", chunk.Choices[0].Delta.Content)
	}
	if inTok != 10 {
		t.Errorf("input tokens: got %d want 10", inTok)
	}
	if outTok != 2 {
		t.Errorf("output tokens: got %d want 2", outTok)
	}
}

func TestGeminiStreamFinalChunk(t *testing.T) {
	payload := map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"parts": []any{map[string]any{"text": "done"}}, "role": "model"},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     float64(10),
			"candidatesTokenCount": float64(5),
		},
	}
	data, _ := json.Marshal(payload)
	line := "data: " + string(data)

	tr := proxy.GeminiTranslator{}
	chunk, _, _, isDone, err := tr.TranslateStreamLine(line, "req-1", "gemini-1.5-pro", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDone {
		t.Error("STOP chunk should signal done")
	}
	if chunk == nil {
		t.Fatal("expected chunk even on final")
	}
	if chunk.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason: got %q want stop", chunk.Choices[0].FinishReason)
	}
}

func TestGeminiStreamSkipsNonDataLine(t *testing.T) {
	tr := proxy.GeminiTranslator{}
	chunk, _, _, isDone, err := tr.TranslateStreamLine("", "req-1", "gemini", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDone || chunk != nil {
		t.Error("blank line should return nil chunk")
	}
}

func TestGeminiStreamDoneMarker(t *testing.T) {
	tr := proxy.GeminiTranslator{}
	_, _, _, isDone, err := tr.TranslateStreamLine("data: [DONE]", "req-1", "gemini", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDone {
		t.Error("[DONE] should signal done")
	}
}

// ---- OpenAI passthrough streaming ------------------------------------------

func TestOpenAIStreamPassthrough(t *testing.T) {
	chunk := proxy.StreamChunk{
		ID:      "chatcmpl-abc",
		Object:  "chat.completion.chunk",
		Created: 1000,
		Model:   "gpt-4o",
		Choices: []proxy.StreamChoice{{
			Index: 0,
			Delta: proxy.Delta{Content: "Hello"},
		}},
	}
	data, _ := json.Marshal(chunk)
	line := "data: " + string(data)

	tr := proxy.OpenAIPassthrough{}
	got, _, _, isDone, err := tr.TranslateStreamLine(line, "req-1", "gpt-4o", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isDone {
		t.Error("non-final chunk should not signal done")
	}
	if got == nil {
		t.Fatal("expected chunk")
	}
	if got.Choices[0].Delta.Content != "Hello" {
		t.Errorf("content: got %q want Hello", got.Choices[0].Delta.Content)
	}
}

func TestOpenAIStreamDoneMarker(t *testing.T) {
	tr := proxy.OpenAIPassthrough{}
	_, _, _, isDone, err := tr.TranslateStreamLine("data: [DONE]", "req-1", "gpt-4o", 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDone {
		t.Error("[DONE] should signal done")
	}
}
