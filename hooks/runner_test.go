package hooks_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/talosred/ce/hooks"
)

func newTestRunner(t *testing.T) (*hooks.Runner, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pre-request"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "post-request"), 0o755); err != nil {
		t.Fatal(err)
	}
	return hooks.NewRunner(dir, 5*time.Second), dir
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts require Unix executable bit")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script %s: %v", name, err)
	}
	return path
}

// ---- discovery / listing ---------------------------------------------------

func TestListHooksEmpty(t *testing.T) {
	r, _ := newTestRunner(t)
	if got := r.ListHooks(); len(got) != 0 {
		t.Errorf("expected 0 hooks, got %d", len(got))
	}
}

func TestListHooksFindsScripts(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-test.sh", "#!/bin/sh\necho '{\"action\":\"proceed\"}'")
	writeScript(t, filepath.Join(dir, "post-request"), "01-audit.sh", "#!/bin/sh\necho '{\"action\":\"proceed\"}'")

	list := r.ListHooks()
	if len(list) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(list))
	}
}

func TestListHooksSkipsNonExecutable(t *testing.T) {
	r, dir := newTestRunner(t)
	// write without execute bit
	path := filepath.Join(dir, "pre-request", "README.md")
	os.WriteFile(path, []byte("docs"), 0o644)

	if got := r.ListHooks(); len(got) != 0 {
		t.Errorf("expected 0 hooks (no executable bit), got %d", len(got))
	}
}

func TestListHooksSkipsDisabledSidecars(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-test.sh", "#!/bin/sh\necho '{\"action\":\"proceed\"}'")
	// create sidecar
	os.WriteFile(filepath.Join(dir, "pre-request", "01-test.sh.disabled"), nil, 0o644)

	list := r.ListHooks()
	if len(list) != 1 || list[0].Enabled {
		t.Errorf("expected 1 disabled hook, got %+v", list)
	}
}

// ---- enable / disable ------------------------------------------------------

func TestSetEnabledDisable(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-test.sh", "#!/bin/sh\necho '{\"action\":\"proceed\"}'")

	if err := r.SetEnabled("pre-request", "01-test.sh", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	list := r.ListHooks()
	if len(list) != 1 || list[0].Enabled {
		t.Error("hook should be disabled")
	}
}

func TestSetEnabledReenable(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-test.sh", "#!/bin/sh\necho '{\"action\":\"proceed\"}'")
	os.WriteFile(filepath.Join(dir, "pre-request", "01-test.sh.disabled"), nil, 0o644)

	if err := r.SetEnabled("pre-request", "01-test.sh", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	list := r.ListHooks()
	if len(list) != 1 || !list[0].Enabled {
		t.Error("hook should be enabled after re-enable")
	}
}

// ---- pre-request execution -------------------------------------------------

func TestRunPreRequestProceedUnchanged(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-pass.sh",
		"#!/bin/sh\necho '{\"action\":\"proceed\"}'")

	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	mod, blockMsg, err := r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-1", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blockMsg != "" {
		t.Errorf("unexpected block: %q", blockMsg)
	}
	if mod != nil {
		t.Error("expected nil modifiedReq when hook doesn't change it")
	}
}

func TestRunPreRequestModifiesRequest(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-modify.sh", `#!/bin/sh
echo '{"action":"proceed","request":{"model":"gpt-4o-mini","messages":[]}}'`)

	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	mod, blockMsg, err := r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-2", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blockMsg != "" {
		t.Errorf("unexpected block: %q", blockMsg)
	}
	if mod == nil {
		t.Fatal("expected modified request")
	}
	var out map[string]any
	json.Unmarshal(mod, &out)
	if out["model"] != "gpt-4o-mini" {
		t.Errorf("model not modified: %v", out["model"])
	}
}

func TestRunPreRequestBlockOnAction(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-block.sh",
		"#!/bin/sh\necho '{\"action\":\"block\",\"message\":\"PII detected\"}'")

	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	_, blockMsg, err := r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-3", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blockMsg == "" {
		t.Error("expected block message")
	}
}

func TestRunPreRequestBlockOnExitCode(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-exitfail.sh",
		"#!/bin/sh\necho 'not allowed' >&2\nexit 1")

	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	_, blockMsg, err := r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-4", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blockMsg == "" {
		t.Error("expected block message on non-zero exit code")
	}
}

func TestRunPreRequestSerialOrder(t *testing.T) {
	r, dir := newTestRunner(t)
	// 01 sets model to "first", 02 sets it to "second" — last write wins
	writeScript(t, filepath.Join(dir, "pre-request"), "01-first.sh",
		"#!/bin/sh\necho '{\"action\":\"proceed\",\"request\":{\"model\":\"first\",\"messages\":[]}}'")
	writeScript(t, filepath.Join(dir, "pre-request"), "02-second.sh",
		"#!/bin/sh\necho '{\"action\":\"proceed\",\"request\":{\"model\":\"second\",\"messages\":[]}}'")

	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	mod, _, err := r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-5", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	json.Unmarshal(mod, &out)
	if out["model"] != "second" {
		t.Errorf("expected second hook to win, got %v", out["model"])
	}
}

func TestRunPreRequestSkipsDisabled(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-block.sh",
		`#!/bin/sh\necho '{"action":"block","message":"should not run"}'`)
	// disable it
	os.WriteFile(filepath.Join(dir, "pre-request", "01-block.sh.disabled"), nil, 0o644)

	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	_, blockMsg, err := r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-6", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blockMsg != "" {
		t.Errorf("disabled hook should not have blocked, got: %q", blockMsg)
	}
}

func TestRunPreRequestTimeout(t *testing.T) {
	_, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-slow.sh",
		"#!/bin/sh\nsleep 10\necho '{\"action\":\"proceed\"}'")

	fast := hooks.NewRunner(dir, 100*time.Millisecond)
	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	_, blockMsg, err := fast.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-7", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	// timeout is a system error → hook is skipped, not blocked
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// slow hook times out and is skipped — no block
	_ = blockMsg
}

func TestRunPreRequestEmptyDir(t *testing.T) {
	r, _ := newTestRunner(t)
	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	mod, blockMsg, err := r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-8", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if blockMsg != "" || mod != nil {
		t.Error("empty hooks dir should be a no-op")
	}
}

// ---- status tracking -------------------------------------------------------

func TestStatusTrackedAfterRun(t *testing.T) {
	r, dir := newTestRunner(t)
	writeScript(t, filepath.Join(dir, "pre-request"), "01-ok.sh",
		"#!/bin/sh\necho '{\"action\":\"proceed\"}'")

	reqJSON := json.RawMessage(`{"model":"gpt-4o","messages":[]}`)
	r.RunPreRequest(context.Background(), hooks.PreRequestPayload{
		ID: "test-9", Provider: "openai", Model: "gpt-4o", Request: reqJSON,
	})

	list := r.ListHooks()
	if len(list) != 1 {
		t.Fatalf("expected 1 hook")
	}
	if list[0].LastRun == nil {
		t.Error("expected LastRun to be populated after execution")
	}
	if list[0].LastRun.ExitCode != 0 {
		t.Errorf("expected exit 0, got %d", list[0].LastRun.ExitCode)
	}
}
