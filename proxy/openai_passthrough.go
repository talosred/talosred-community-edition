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

const openaiAPIBase = "https://api.openai.com"

type OpenAIPassthrough struct{}

func (t *OpenAIPassthrough) BuildRequest(req *ChatRequest) (*http.Request, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, openaiAPIBase+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	return httpReq, nil
}

func (t *OpenAIPassthrough) TranslateResponse(body []byte, reqID, model string, created int64) (*ChatResponse, error) {
	var resp ChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	return &resp, nil
}

func (t *OpenAIPassthrough) TranslateStreamLine(line, reqID, model string, created int64) (chunk *StreamChunk, inputTok, outputTok int64, isDone bool, err error) {
	if !strings.HasPrefix(line, "data: ") {
		return nil, 0, 0, false, nil
	}
	data := strings.TrimPrefix(line, "data: ")
	if data == "[DONE]" {
		return nil, 0, 0, true, nil
	}

	var sc StreamChunk
	if err := json.Unmarshal([]byte(data), &sc); err != nil {
		return nil, 0, 0, false, fmt.Errorf("decode openai stream chunk: %w", err)
	}

	// OpenAI sends usage in the last chunk via stream_options; skip if empty.
	if len(sc.Choices) == 0 {
		return nil, 0, 0, false, nil
	}

	if len(sc.Choices) > 0 && sc.Choices[0].FinishReason == "stop" {
		isDone = true
	}

	return &sc, 0, 0, isDone, nil
}

func (t *OpenAIPassthrough) Timeout() time.Duration {
	return 5 * time.Minute
}
