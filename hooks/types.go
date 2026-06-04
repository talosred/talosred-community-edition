package hooks

import (
	"encoding/json"
	"time"
)

// PreRequestPayload is sent on stdin to every pre-request hook.
type PreRequestPayload struct {
	ID       string          `json:"id"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Request  json.RawMessage `json:"request"` // OpenAI ChatRequest JSON
}

// PostRequestPayload is sent on stdin to every post-request hook.
type PostRequestPayload struct {
	ID       string          `json:"id"`
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Request  json.RawMessage `json:"request"`  // original ChatRequest JSON
	Response json.RawMessage `json:"response"` // ChatResponse JSON (nil for streaming)
	Metrics  PostMetrics     `json:"metrics"`
}

type PostMetrics struct {
	InputTok  int64   `json:"input_tok"`
	OutputTok int64   `json:"output_tok"`
	TTFTms    int64   `json:"ttft_ms"`
	LatencyMs int64   `json:"latency_ms"`
	CostUSD   float64 `json:"cost_usd"`
}

// HookResult is the JSON a hook writes to stdout.
type HookResult struct {
	// Action must be "proceed" or "block".
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
	// Request may contain a modified ChatRequest (pre-request hooks only).
	Request json.RawMessage `json:"request,omitempty"`
}

// HookInfo describes a discovered hook file.
type HookInfo struct {
	Name    string // filename without directory
	Type    string // "pre-request" or "post-request"
	Path    string // absolute path
	Enabled bool
	LastRun *RunStatus
}

// RunStatus is the result of the last execution of a hook.
type RunStatus struct {
	At       time.Time
	ExitCode int
	Duration time.Duration
	Stderr   string // last 512 bytes
	Err      string // system-level error (timeout, not-found, etc.)
}
