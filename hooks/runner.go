package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout   = 5 * time.Second
	maxStderrCapture = 512
)

// Runner discovers and executes hook scripts.
type Runner struct {
	hooksDir string
	timeout  time.Duration

	mu       sync.RWMutex
	statuses map[string]*RunStatus // key: "type/name"
}

func NewRunner(hooksDir string, timeout time.Duration) *Runner {
	if timeout == 0 {
		timeout = defaultTimeout
	}
	return &Runner{
		hooksDir: hooksDir,
		timeout:  timeout,
		statuses: make(map[string]*RunStatus),
	}
}

// RunPreRequest executes all pre-request hooks in alphabetical order.
// Returns the (possibly modified) request JSON and an optional block message.
// A non-empty blockMsg means the request must be rejected.
// modifiedReq is nil when no hook changed the request.
func (r *Runner) RunPreRequest(ctx context.Context, p PreRequestPayload) (modifiedReq json.RawMessage, blockMsg string, err error) {
	hookPaths, err := r.discover("pre-request")
	if err != nil {
		return nil, "", fmt.Errorf("discover pre-request hooks: %w", err)
	}

	currentReq := p.Request
	modified := false

	for _, h := range hookPaths {
		p.Request = currentReq
		payload, _ := json.Marshal(p)

		result, status := r.exec(ctx, h, payload)
		r.saveStatus("pre-request", filepath.Base(h), status)

		if status.Err != "" {
			log.Printf("hook %s error: %s (skipping)", filepath.Base(h), status.Err)
			continue
		}
		if status.Stderr != "" {
			log.Printf("hook %s stderr: %s", filepath.Base(h), status.Stderr)
		}

		if result == nil {
			if status.ExitCode != 0 {
				msg := strings.TrimSpace(status.Stderr)
				if msg == "" {
					msg = fmt.Sprintf("hook %s exited %d", filepath.Base(h), status.ExitCode)
				}
				return nil, msg, nil
			}
			continue
		}

		if result.Action == "block" || status.ExitCode != 0 {
			msg := result.Message
			if msg == "" {
				msg = strings.TrimSpace(status.Stderr)
			}
			if msg == "" {
				msg = fmt.Sprintf("blocked by hook %s", filepath.Base(h))
			}
			return nil, msg, nil
		}

		if result.Request != nil {
			currentReq = result.Request
			modified = true
		}
	}

	if !modified {
		return nil, "", nil
	}
	return currentReq, "", nil
}

// RunPostRequestAsync fires post-request hooks concurrently in the background.
// Each hook gets its own goroutine; errors are logged, never surfaced to caller.
func (r *Runner) RunPostRequestAsync(p PostRequestPayload) {
	hooks, err := r.discover("post-request")
	if err != nil {
		log.Printf("discover post-request hooks: %v", err)
		return
	}

	for _, h := range hooks {
		go func(h string) {
			payload, _ := json.Marshal(p)
			ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
			defer cancel()

			_, status := r.exec(ctx, h, payload)
			r.saveStatus("post-request", filepath.Base(h), status)

			if status.Err != "" {
				log.Printf("post-request hook %s: %s", filepath.Base(h), status.Err)
			}
			if status.Stderr != "" {
				log.Printf("post-request hook %s stderr: %s", filepath.Base(h), status.Stderr)
			}
		}(h)
	}
}

// ListHooks returns all discovered hooks (enabled and disabled) for both types.
func (r *Runner) ListHooks() []HookInfo {
	var out []HookInfo
	for _, hookType := range []string{"pre-request", "post-request"} {
		dir := filepath.Join(r.hooksDir, hookType)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			// skip sidecar .disabled files
			if strings.HasSuffix(name, ".disabled") {
				continue
			}
			path := filepath.Join(dir, name)
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0o111 == 0 {
				continue // not executable
			}
			enabled := !r.isDisabled(path)

			r.mu.RLock()
			status := r.statuses[hookType+"/"+name]
			r.mu.RUnlock()

			out = append(out, HookInfo{
				Name:    name,
				Type:    hookType,
				Path:    path,
				Enabled: enabled,
				LastRun: status,
			})
		}
	}
	return out
}

// SetEnabled creates or removes the sidecar .disabled file.
func (r *Runner) SetEnabled(hookType, name string, enabled bool) error {
	sidecar := filepath.Join(r.hooksDir, hookType, name+".disabled")
	if enabled {
		err := os.Remove(sidecar)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove .disabled: %w", err)
		}
		return nil
	}
	f, err := os.Create(sidecar)
	if err != nil {
		return fmt.Errorf("create .disabled: %w", err)
	}
	return f.Close()
}

// HooksDir returns the configured hooks directory.
func (r *Runner) HooksDir() string { return r.hooksDir }

// ---- internals -------------------------------------------------------------

func (r *Runner) discover(hookType string) ([]string, error) {
	dir := filepath.Join(r.hooksDir, hookType)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".disabled") {
			continue
		}
		path := filepath.Join(dir, name)
		if r.isDisabled(path) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// must be executable
		if info.Mode()&0o111 == 0 {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (r *Runner) isDisabled(hookPath string) bool {
	_, err := os.Stat(hookPath + ".disabled")
	return err == nil
}

func (r *Runner) exec(ctx context.Context, hookPath string, payload []byte) (*HookResult, *RunStatus) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	start := time.Now()
	status := &RunStatus{At: start}

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Stdin = bytes.NewReader(payload)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	status.Duration = time.Since(start)

	// capture stderr (last maxStderrCapture bytes)
	if se := stderrBuf.String(); se != "" {
		if len(se) > maxStderrCapture {
			se = "…" + se[len(se)-maxStderrCapture:]
		}
		status.Stderr = strings.TrimSpace(se)
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			status.ExitCode = exitErr.ExitCode()
		} else {
			// system error: timeout, not found, permission denied
			status.Err = runErr.Error()
			status.ExitCode = -1
			return nil, status
		}
	}

	// parse stdout
	out := bytes.TrimSpace(stdoutBuf.Bytes())
	if len(out) == 0 {
		return nil, status
	}

	var result HookResult
	if err := json.Unmarshal(out, &result); err != nil {
		status.Err = fmt.Sprintf("invalid JSON output: %v", err)
		return nil, status
	}
	return &result, status
}

func (r *Runner) saveStatus(hookType, name string, s *RunStatus) {
	r.mu.Lock()
	r.statuses[hookType+"/"+name] = s
	r.mu.Unlock()
}
