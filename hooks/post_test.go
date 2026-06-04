package hooks_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/talosred/ce/hooks"
)

func TestHooksDir(t *testing.T) {
	r := hooks.NewRunner("/some/dir", 0)
	if r.HooksDir() != "/some/dir" {
		t.Errorf("HooksDir: got %q", r.HooksDir())
	}
}

func TestRunPostRequestAsyncExecutes(t *testing.T) {
	r, dir := newTestRunner(t)
	// hook writes a marker file so we can confirm it ran
	marker := filepath.Join(t.TempDir(), "ran")
	writeScript(t, filepath.Join(dir, "post-request"), "01-mark.sh",
		"#!/bin/sh\ncat > /dev/null\ntouch "+marker+"\necho '{\"action\":\"proceed\"}'")

	r.RunPostRequestAsync(hooks.PostRequestPayload{
		ID: "p1", Provider: "openai", Model: "gpt-4o",
		Request:  json.RawMessage(`{}`),
		Response: json.RawMessage(`{}`),
		Metrics:  hooks.PostMetrics{InputTok: 1, OutputTok: 1},
	})

	// async — poll for the marker + recorded status
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("post-request hook did not run")
	}

	// status should be recorded
	for time.Now().Before(deadline) {
		list := r.ListHooks()
		if len(list) == 1 && list[0].LastRun != nil {
			if list[0].LastRun.ExitCode != 0 {
				t.Errorf("exit code: got %d want 0", list[0].LastRun.ExitCode)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("post-request hook status not recorded")
}

func TestRunPostRequestAsyncNoHooks(t *testing.T) {
	r, _ := newTestRunner(t)
	// must not panic with an empty post-request dir
	r.RunPostRequestAsync(hooks.PostRequestPayload{ID: "p", Provider: "openai", Model: "gpt-4o"})
}
