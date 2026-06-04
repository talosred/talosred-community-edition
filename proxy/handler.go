package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/google/uuid"
	"github.com/talosred/ce/hooks"
	"github.com/talosred/ce/metrics"
	"github.com/talosred/ce/store"
)

const maxAttempts = 3 // 1 initial + 2 retries on 429

// Backoff bounds — vars (not consts) so tests can shrink them.
var (
	maxRetryWait  = 30 * time.Second
	baseRetryWait = 2 * time.Second
)

// translator is the provider-specific request/response adapter.
type translator interface {
	BuildRequest(req *ChatRequest) (*http.Request, error)
	TranslateResponse(body []byte, reqID, model string, created int64) (*ChatResponse, error)
	TranslateStreamLine(line, reqID, model string, created int64) (chunk *StreamChunk, inputTok, outputTok int64, isDone bool, err error)
	Timeout() time.Duration
}

type Handler struct {
	store    *store.Store
	cost     *metrics.CostCalculator
	hooks    *hooks.Runner // nil = hooks disabled
	proxyKey string        // "" = key vaulting auth disabled
	client   *http.Client
}

func NewHandler(s *store.Store, c *metrics.CostCalculator, hr *hooks.Runner, proxyKey string) *Handler {
	return &Handler{
		store:    s,
		cost:     c,
		hooks:    hr,
		proxyKey: proxyKey,
		client:   &http.Client{Timeout: 10 * time.Minute},
	}
}

// reqMeta carries per-request metadata through the handler pipeline.
type reqMeta struct {
	id       string
	provider string
	created  int64
	app      string
	user     string
	reqBody  []byte // final (post-hook) OpenAI-format request
	start    time.Time
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	// ---- key vaulting (Pain 2): clients use a dummy key; real keys live in
	// the proxy env only. When --proxy-key is set, reject mismatched callers.
	if h.proxyKey != "" {
		if extractBearer(r.Header.Get("Authorization")) != h.proxyKey {
			writeError(w, http.StatusUnauthorized, "invalid_proxy_key",
				"missing or invalid proxy key — use the dummy key configured via --proxy-key")
			return
		}
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// ---- attribution (Pain 1) ----------------------------------------------
	appName := r.Header.Get("X-Talos-App")
	userName := r.Header.Get("X-Talos-User")

	// ---- model aliasing (Pain 5): redirect e.g. gpt-3.5-turbo -> local Ollama
	provider := r.Header.Get("X-Provider")
	aliasURL := ""
	if h.store != nil {
		if alias, err := h.store.MatchAlias(req.Model); err != nil {
			log.Printf("alias lookup: %v", err)
		} else if alias != nil {
			log.Printf("alias %q -> %q @ %q (%s)", req.Model, alias.TargetModel, alias.TargetURL, alias.Provider)
			req.Model = alias.TargetModel
			provider = alias.Provider
			aliasURL = alias.TargetURL
		}
	}
	if provider == "" {
		provider = DetectProvider(req.Model)
	}

	var t translator
	switch provider {
	case "anthropic":
		t = &AnthropicTranslator{BaseURL: aliasURL}
	case "gemini":
		t = &GeminiTranslator{BaseURL: aliasURL}
	default:
		t = &OpenAIPassthrough{BaseURL: aliasURL}
	}

	reqID := "talos-" + uuid.New().String()
	created := time.Now().Unix()

	ctx, span := otel.Tracer("talosred").Start(r.Context(), "proxy.chat")
	defer span.End()
	span.SetAttributes(
		attribute.String("llm.provider", provider),
		attribute.String("llm.model", req.Model),
		attribute.String("talos.app", appName),
		attribute.String("talos.user", userName),
	)

	// ---- pre-request hooks -------------------------------------------------
	if h.hooks != nil {
		reqJSON, _ := json.Marshal(req)
		modifiedReq, blockMsg, err := h.hooks.RunPreRequest(ctx, hooks.PreRequestPayload{
			ID:       reqID,
			Provider: provider,
			Model:    req.Model,
			Request:  json.RawMessage(reqJSON),
		})
		if err != nil {
			log.Printf("pre-request hooks: %v", err)
		}
		if blockMsg != "" {
			writeError(w, http.StatusForbidden, "blocked_by_hook", blockMsg)
			return
		}
		if modifiedReq != nil {
			if err := json.Unmarshal(modifiedReq, &req); err != nil {
				log.Printf("apply hook-modified request: %v", err)
			}
		}
	}

	reqBody, _ := json.Marshal(req)
	m := &reqMeta{
		id:       reqID,
		provider: provider,
		created:  created,
		app:      appName,
		user:     userName,
		reqBody:  reqBody,
		start:    time.Now(),
	}

	if req.Stream {
		h.handleStream(w, r.WithContext(ctx), &req, t, m, span)
	} else {
		h.handleFull(w, r.WithContext(ctx), &req, t, m, span)
	}
}

func (h *Handler) handleFull(
	w http.ResponseWriter, r *http.Request,
	req *ChatRequest, t translator, m *reqMeta, span trace.Span,
) {
	resp, meta, retries, err := h.doUpstream(r.Context(), t, req)
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

	latency := time.Since(m.start).Milliseconds()

	// Non-2xx: still log it so failed requests are inspectable (Pain 3).
	if resp.StatusCode != http.StatusOK {
		go h.logRequest(&store.RequestLog{
			ID: m.id, TS: time.Now(), Provider: m.provider, Model: req.Model,
			LatencyMs: latency, TTFTms: latency,
			AppName: m.app, UserName: m.user,
			StatusCode: resp.StatusCode, Retries: retries,
			ReqJSON: string(m.reqBody), ResJSON: string(body),
			UpstreamURL: meta.url, UpstreamHeaders: meta.headers, UpstreamBody: meta.body,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	chatResp, err := t.TranslateResponse(body, m.id, req.Model, m.created)
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
		attribute.Int("llm.retries", retries),
	)

	outBody, _ := json.Marshal(chatResp)

	go h.logRequest(&store.RequestLog{
		ID: m.id, TS: time.Now(), Provider: m.provider, Model: req.Model,
		InputTok: chatResp.Usage.PromptTokens, OutputTok: chatResp.Usage.CompletionTokens,
		TTFTms: latency, LatencyMs: latency, CostUSD: costUSD,
		AppName: m.app, UserName: m.user,
		StatusCode: resp.StatusCode, Retries: retries,
		ReqJSON: string(m.reqBody), ResJSON: string(body),
		UpstreamURL: meta.url, UpstreamHeaders: meta.headers, UpstreamBody: meta.body,
	})

	if h.hooks != nil {
		resJSON, _ := json.Marshal(chatResp)
		h.hooks.RunPostRequestAsync(hooks.PostRequestPayload{
			ID: m.id, Provider: m.provider, Model: req.Model,
			Request:  json.RawMessage(m.reqBody),
			Response: json.RawMessage(resJSON),
			Metrics: hooks.PostMetrics{
				InputTok:  chatResp.Usage.PromptTokens,
				OutputTok: chatResp.Usage.CompletionTokens,
				TTFTms:    latency,
				LatencyMs: latency,
				CostUSD:   costUSD,
			},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(outBody)
}

func (h *Handler) handleStream(
	w http.ResponseWriter, r *http.Request,
	req *ChatRequest, t translator, m *reqMeta, span trace.Span,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_error", "streaming not supported")
		return
	}

	resp, meta, retries, err := h.doUpstream(r.Context(), t, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		latency := time.Since(m.start).Milliseconds()
		go h.logRequest(&store.RequestLog{
			ID: m.id, TS: time.Now(), Provider: m.provider, Model: req.Model,
			LatencyMs: latency, TTFTms: latency,
			AppName: m.app, UserName: m.user,
			StatusCode: resp.StatusCode, Retries: retries,
			ReqJSON: string(m.reqBody), ResJSON: string(body),
			UpstreamURL: meta.url, UpstreamHeaders: meta.headers, UpstreamBody: meta.body,
		})
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

		chunk, inTok, outTok, isDone, err := t.TranslateStreamLine(line, m.id, req.Model, m.created)
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
				ttftMs = time.Since(m.start).Milliseconds()
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

	latency := time.Since(m.start).Milliseconds()
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
		attribute.Int("llm.retries", retries),
	)

	go h.logRequest(&store.RequestLog{
		ID: m.id, TS: time.Now(), Provider: m.provider, Model: req.Model,
		InputTok: inputTok, OutputTok: outputTok,
		TTFTms: ttftMs, LatencyMs: latency, CostUSD: costUSD,
		AppName: m.app, UserName: m.user,
		StatusCode: resp.StatusCode, Retries: retries,
		ReqJSON: string(m.reqBody), ResJSON: resBuf.String(),
		UpstreamURL: meta.url, UpstreamHeaders: meta.headers, UpstreamBody: meta.body,
	})

	if h.hooks != nil {
		h.hooks.RunPostRequestAsync(hooks.PostRequestPayload{
			ID: m.id, Provider: m.provider, Model: req.Model,
			Request:  json.RawMessage(m.reqBody),
			Response: json.RawMessage(resBuf.Bytes()),
			Metrics: hooks.PostMetrics{
				InputTok:  inputTok,
				OutputTok: outputTok,
				TTFTms:    ttftMs,
				LatencyMs: latency,
				CostUSD:   costUSD,
			},
		})
	}
}

// upstreamMeta is the captured, redacted upstream request for "Copy as cURL".
type upstreamMeta struct {
	url     string
	headers string // redacted JSON object
	body    string // exact translated body sent upstream
}

// doUpstream builds and sends the upstream request, retrying on 429 (Pain 4).
// It returns the final response, captured upstream metadata, and retry count.
func (h *Handler) doUpstream(ctx context.Context, t translator, req *ChatRequest) (*http.Response, upstreamMeta, int, error) {
	var meta upstreamMeta
	retries := 0

	for attempt := range maxAttempts {
		upReq, err := t.BuildRequest(req)
		if err != nil {
			return nil, meta, retries, err
		}
		upReq = upReq.WithContext(ctx)
		meta = captureUpstreamMeta(upReq)

		resp, err := h.client.Do(upReq)
		if err != nil {
			return nil, meta, retries, err
		}

		if resp.StatusCode != http.StatusTooManyRequests || attempt == maxAttempts-1 {
			return resp, meta, retries, nil
		}

		// 429 — back off and retry.
		wait := retryWait(resp, attempt)
		_ = resp.Body.Close()
		retries++
		log.Printf("upstream 429, retry %d/%d after %s", retries, maxAttempts-1, wait)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, meta, retries, ctx.Err()
		}
	}

	// unreachable — loop always returns on the last attempt
	return nil, meta, retries, fmt.Errorf("retry loop exhausted")
}

// retryWait honours the upstream Retry-After header, falling back to a simple
// linear backoff (2s, 4s, …) capped at maxRetryWait.
func retryWait(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs > 0 {
			d := time.Duration(secs) * time.Second
			if d > maxRetryWait {
				return maxRetryWait
			}
			return d
		}
	}
	d := time.Duration(attempt+1) * baseRetryWait
	if d > maxRetryWait {
		return maxRetryWait
	}
	return d
}

// captureUpstreamMeta reads the request body (restoring it for the real send)
// and records the redacted URL and headers for later cURL reproduction.
func captureUpstreamMeta(req *http.Request) upstreamMeta {
	var body string
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.ContentLength = int64(len(b))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(b)), nil
		}
		body = string(b)
	}
	return upstreamMeta{
		url:     redactURL(req.URL),
		headers: redactHeaders(req.Header),
		body:    body,
	}
}

func redactURL(u *url.URL) string {
	q := u.Query()
	if q.Get("key") != "" {
		q.Set("key", "REDACTED")
		clone := *u
		clone.RawQuery = q.Encode()
		return clone.String()
	}
	return u.String()
}

func redactHeaders(h http.Header) string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		val := strings.Join(v, ", ")
		switch strings.ToLower(k) {
		case "authorization", "x-api-key":
			val = "REDACTED"
		}
		out[k] = val
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func extractBearer(authHeader string) string {
	if authHeader == "" {
		return ""
	}
	if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(authHeader)
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
