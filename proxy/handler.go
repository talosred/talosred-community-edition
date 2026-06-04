package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/google/uuid"
	"github.com/talosred/ce/metrics"
	"github.com/talosred/ce/store"
)

// translator is the provider-specific request/response adapter.
type translator interface {
	BuildRequest(req *ChatRequest) (*http.Request, error)
	TranslateResponse(body []byte, reqID, model string, created int64) (*ChatResponse, error)
	TranslateStreamLine(line, reqID, model string, created int64) (chunk *StreamChunk, inputTok, outputTok int64, isDone bool, err error)
	Timeout() time.Duration
}

type Handler struct {
	store  *store.Store
	cost   *metrics.CostCalculator
	client *http.Client
}

func NewHandler(s *store.Store, c *metrics.CostCalculator) *Handler {
	return &Handler{
		store:  s,
		cost:   c,
		client: &http.Client{Timeout: 10 * time.Minute},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// Allow override via header; otherwise detect from model name.
	provider := r.Header.Get("X-Provider")
	if provider == "" {
		provider = DetectProvider(req.Model)
	}

	var t translator
	switch provider {
	case "anthropic":
		t = &AnthropicTranslator{}
	case "gemini":
		t = &GeminiTranslator{}
	default:
		t = &OpenAIPassthrough{}
	}

	reqID := "talos-" + uuid.New().String()
	created := time.Now().Unix()
	reqBody, _ := json.Marshal(req)

	ctx, span := otel.Tracer("talosred").Start(r.Context(), "proxy.chat")
	defer span.End()

	span.SetAttributes(
		attribute.String("llm.provider", provider),
		attribute.String("llm.model", req.Model),
	)

	start := time.Now()

	if req.Stream {
		h.handleStream(w, r.WithContext(ctx), &req, t, provider, reqID, created, reqBody, start, span)
	} else {
		h.handleFull(w, r.WithContext(ctx), &req, t, provider, reqID, created, reqBody, start, span)
	}
}

func (h *Handler) handleFull(
	w http.ResponseWriter, r *http.Request,
	req *ChatRequest, t translator,
	provider, reqID string, created int64,
	reqBody []byte, start time.Time, span trace.Span,
) {
	upReq, err := t.BuildRequest(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upstream_error", err.Error())
		return
	}
	upReq = upReq.WithContext(r.Context())

	resp, err := h.client.Do(upReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "read upstream body: "+err.Error())
		return
	}

	latency := time.Since(start).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	chatResp, err := t.TranslateResponse(body, reqID, req.Model, created)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "translation_error", err.Error())
		return
	}

	costUSD := h.cost.Calculate(req.Model, chatResp.Usage.PromptTokens, chatResp.Usage.CompletionTokens)

	span.SetAttributes(
		attribute.Int64("llm.input_tokens", chatResp.Usage.PromptTokens),
		attribute.Int64("llm.output_tokens", chatResp.Usage.CompletionTokens),
		attribute.Float64("llm.cost_usd", costUSD),
		attribute.Int64("llm.latency_ms", latency),
	)

	outBody, _ := json.Marshal(chatResp)

	go h.logRequest(&store.RequestLog{
		ID:        reqID,
		TS:        time.Now(),
		Provider:  provider,
		Model:     req.Model,
		InputTok:  chatResp.Usage.PromptTokens,
		OutputTok: chatResp.Usage.CompletionTokens,
		TTFTms:    latency,
		LatencyMs: latency,
		CostUSD:   costUSD,
		ReqJSON:   string(reqBody),
		ResJSON:   string(body),
	})

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(outBody)
}

func (h *Handler) handleStream(
	w http.ResponseWriter, r *http.Request,
	req *ChatRequest, t translator,
	provider, reqID string, created int64,
	reqBody []byte, start time.Time, span trace.Span,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_error", "streaming not supported")
		return
	}

	upReq, err := t.BuildRequest(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "upstream_error", err.Error())
		return
	}
	upReq = upReq.WithContext(r.Context())

	resp, err := h.client.Do(upReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	var (
		ttftMs    int64 = -1
		inputTok  int64
		outputTok int64
		resBuf    bytes.Buffer
	)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		chunk, inTok, outTok, isDone, err := t.TranslateStreamLine(line, reqID, req.Model, created)
		if err != nil {
			log.Printf("stream translate: %v", err)
			continue
		}

		if inTok > 0 {
			inputTok = inTok
		}
		if outTok > 0 {
			outputTok = outTok
		}

		if chunk != nil {
			if ttftMs < 0 {
				ttftMs = time.Since(start).Milliseconds()
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			resBuf.Write(data)
			flusher.Flush()
		}

		if isDone {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("stream scan error: %v", err)
	}

	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	latency := time.Since(start).Milliseconds()
	if ttftMs < 0 {
		ttftMs = latency
	}

	costUSD := h.cost.Calculate(req.Model, inputTok, outputTok)

	span.SetAttributes(
		attribute.Int64("llm.input_tokens", inputTok),
		attribute.Int64("llm.output_tokens", outputTok),
		attribute.Float64("llm.cost_usd", costUSD),
		attribute.Int64("llm.ttft_ms", ttftMs),
		attribute.Int64("llm.latency_ms", latency),
	)

	go h.logRequest(&store.RequestLog{
		ID:        reqID,
		TS:        time.Now(),
		Provider:  provider,
		Model:     req.Model,
		InputTok:  inputTok,
		OutputTok: outputTok,
		TTFTms:    ttftMs,
		LatencyMs: latency,
		CostUSD:   costUSD,
		ReqJSON:   string(reqBody),
		ResJSON:   resBuf.String(),
	})
}

func (h *Handler) logRequest(r *store.RequestLog) {
	if err := h.store.Insert(r); err != nil {
		log.Printf("store insert: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorDetail{Type: errType, Message: msg},
	})
}
