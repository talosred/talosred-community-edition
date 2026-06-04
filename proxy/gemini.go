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

const geminiAPIBase = "https://generativelanguage.googleapis.com/v1beta/models"

// ---- Gemini request types --------------------------------------------------

type geminiRequest struct {
	Contents          []geminiContent    `json:"contents"`
	SystemInstruction *geminiContent     `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig   `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// ---- Gemini response types -------------------------------------------------

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

type geminiUsage struct {
	PromptTokenCount     int64 `json:"promptTokenCount"`
	CandidatesTokenCount int64 `json:"candidatesTokenCount"`
	TotalTokenCount      int64 `json:"totalTokenCount"`
}

// ---- Translator ------------------------------------------------------------

type GeminiTranslator struct {
	BaseURL string
}

func (t *GeminiTranslator) BuildRequest(req *ChatRequest) (*http.Request, error) {
	base := t.BaseURL
	if base == "" {
		base = geminiAPIBase
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		if t.BaseURL == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY or GOOGLE_API_KEY not set")
		}
		apiKey = "local"
	}

	gr := geminiRequest{
		GenerationConfig: &geminiGenConfig{
			MaxOutputTokens: req.MaxTokens,
			Temperature:     req.Temperature,
			TopP:            req.TopP,
			StopSequences:   req.Stop,
		},
	}

	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			gr.SystemInstruction = &geminiContent{
				Parts: []geminiPart{{Text: m.Content}},
			}
		case "assistant":
			gr.Contents = append(gr.Contents, geminiContent{
				Role:  "model",
				Parts: []geminiPart{{Text: m.Content}},
			})
		default:
			gr.Contents = append(gr.Contents, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{Text: m.Content}},
			})
		}
	}

	body, err := json.Marshal(gr)
	if err != nil {
		return nil, err
	}

	action := "generateContent"
	if req.Stream {
		action = "streamGenerateContent"
	}
	url := fmt.Sprintf("%s/%s:%s?key=%s", strings.TrimRight(base, "/"), req.Model, action, apiKey)
	if req.Stream {
		url += "&alt=sse"
	}

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return httpReq, nil
}

func (t *GeminiTranslator) TranslateResponse(body []byte, reqID string, model string, created int64) (*ChatResponse, error) {
	var gr geminiResponse
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("decode gemini response: %w", err)
	}

	if len(gr.Candidates) == 0 {
		return nil, fmt.Errorf("gemini returned no candidates")
	}

	var sb strings.Builder
	for _, part := range gr.Candidates[0].Content.Parts {
		sb.WriteString(part.Text)
	}

	finishReason := "stop"
	if r := gr.Candidates[0].FinishReason; r != "" && r != "STOP" {
		finishReason = strings.ToLower(r)
	}

	return &ChatResponse{
		ID:      reqID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: sb.String()},
			FinishReason: finishReason,
		}},
		Usage: Usage{
			PromptTokens:     gr.UsageMetadata.PromptTokenCount,
			CompletionTokens: gr.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      gr.UsageMetadata.TotalTokenCount,
		},
	}, nil
}

// TranslateStreamLine converts a single Gemini SSE data line into an
// OpenAI-compatible StreamChunk. Gemini streaming sends full response objects
// per chunk, not deltas.
func (t *GeminiTranslator) TranslateStreamLine(line string, reqID string, model string, created int64) (chunk *StreamChunk, inputTok, outputTok int64, isDone bool, err error) {
	if !strings.HasPrefix(line, "data: ") {
		return nil, 0, 0, false, nil
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return nil, 0, 0, true, nil
	}

	var gr geminiResponse
	if err := json.Unmarshal([]byte(data), &gr); err != nil {
		return nil, 0, 0, false, fmt.Errorf("decode gemini stream chunk: %w", err)
	}

	inputTok = gr.UsageMetadata.PromptTokenCount
	outputTok = gr.UsageMetadata.CandidatesTokenCount

	if len(gr.Candidates) == 0 {
		return nil, inputTok, outputTok, false, nil
	}

	var sb strings.Builder
	for _, part := range gr.Candidates[0].Content.Parts {
		sb.WriteString(part.Text)
	}

	finishReason := ""
	if r := gr.Candidates[0].FinishReason; r == "STOP" || r == "MAX_TOKENS" {
		finishReason = strings.ToLower(r)
		if r == "STOP" {
			finishReason = "stop"
		}
	}

	isDone = finishReason != ""

	return &StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        Delta{Content: sb.String()},
			FinishReason: finishReason,
		}},
	}, inputTok, outputTok, isDone, nil
}

func (t *GeminiTranslator) Timeout() time.Duration {
	return 5 * time.Minute
}
