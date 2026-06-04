package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const anthropicAPIBase = "https://api.anthropic.com"
const anthropicVersion = "2023-06-01"

// ---- Anthropic request types -----------------------------------------------

type anthropicRequest struct {
	Model     string             `json:"model"`
	Messages  []anthropicMessage `json:"messages"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	TopP        *float64         `json:"top_p,omitempty"`
	StopSequences []string       `json:"stop_sequences,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ---- Anthropic response types -----------------------------------------------

type anthropicResponse struct {
	ID      string             `json:"id"`
	Type    string             `json:"type"`
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
	Model   string             `json:"model"`
	Usage   anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// ---- Streaming event types --------------------------------------------------

type anthropicStreamEvent struct {
	Type    string          `json:"type"`
	Message *anthropicResponse `json:"message,omitempty"`
	Index   int             `json:"index"`
	Delta   *anthropicDelta `json:"delta,omitempty"`
	Usage   *anthropicUsage `json:"usage,omitempty"`
}

type anthropicDelta struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

// ---- Translator ------------------------------------------------------------

type AnthropicTranslator struct{}

func (t *AnthropicTranslator) BuildRequest(req *ChatRequest) (*http.Request, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	ar := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}
	if ar.MaxTokens == 0 {
		ar.MaxTokens = 4096
	}
	if len(req.Stop) > 0 {
		ar.StopSequences = req.Stop
	}

	for _, m := range req.Messages {
		if m.Role == "system" {
			ar.System = m.Content
			continue
		}
		ar.Messages = append(ar.Messages, anthropicMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	body, err := json.Marshal(ar)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, anthropicAPIBase+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	return httpReq, nil
}

func (t *AnthropicTranslator) TranslateResponse(body []byte, reqID string, model string, created int64) (*ChatResponse, error) {
	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}

	var sb strings.Builder
	for _, c := range ar.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	text := sb.String()

	return &ChatResponse{
		ID:      reqID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: text},
			FinishReason: "stop",
		}},
		Usage: Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}, nil
}

// TranslateStreamLine converts a single Anthropic SSE data line into an
// OpenAI-compatible StreamChunk. Returns nil, nil for events to skip.
// isDone=true signals stream end.
func (t *AnthropicTranslator) TranslateStreamLine(line string, reqID string, model string, created int64) (chunk *StreamChunk, inputTok, outputTok int64, isDone bool, err error) {
	if !strings.HasPrefix(line, "data: ") {
		return nil, 0, 0, false, nil
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return nil, 0, 0, true, nil
	}

	var event anthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil, 0, 0, false, fmt.Errorf("decode stream event: %w", err)
	}

	switch event.Type {
	case "message_start":
		if event.Message != nil {
			inputTok = event.Message.Usage.InputTokens
		}
		// emit role delta
		return &StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: Delta{Role: "assistant"},
			}},
		}, inputTok, 0, false, nil

	case "content_block_delta":
		if event.Delta == nil || event.Delta.Type != "text_delta" {
			return nil, 0, 0, false, nil
		}
		return &StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: Delta{Content: event.Delta.Text},
			}},
		}, 0, 0, false, nil

	case "message_delta":
		if event.Usage != nil {
			outputTok = event.Usage.OutputTokens
		}
		return &StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []StreamChoice{{
				Index:        0,
				Delta:        Delta{},
				FinishReason: "stop",
			}},
		}, 0, outputTok, false, nil

	case "message_stop":
		return nil, 0, 0, true, nil
	}

	return nil, 0, 0, false, nil
}

func (t *AnthropicTranslator) Timeout() time.Duration {
	return 5 * time.Minute
}
