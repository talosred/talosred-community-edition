package dashboard

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/talosred/ce/store"
)

func TestSSEEscape(t *testing.T) {
	in := "line1\nline2\r\n<tr>"
	got := sseEscape(in)
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("sseEscape left newlines: %q", got)
	}
	if !strings.Contains(got, "&#10;") {
		t.Errorf("sseEscape did not encode newline: %q", got)
	}
}

func TestParseFilter(t *testing.T) {
	r := httptest.NewRequest("GET", "/ui/requests?provider=openai&model=gpt-4o&app=billing&user=dave", nil)
	f := parseFilter(r)
	if f.Provider != "openai" || f.Model != "gpt-4o" || f.App != "billing" || f.User != "dave" {
		t.Errorf("parseFilter wrong: %+v", f)
	}
	if f.Limit != 100 {
		t.Errorf("default limit: got %d want 100", f.Limit)
	}
}

func TestCurlCommand(t *testing.T) {
	r := &store.RequestLog{
		UpstreamURL:     "https://api.openai.com/v1/chat/completions",
		UpstreamHeaders: `{"Authorization":"REDACTED","Content-Type":"application/json"}`,
		UpstreamBody:    `{"model":"gpt-4o"}`,
	}
	cmd := curlCommand(r)
	for _, want := range []string{"curl -X POST", "api.openai.com", "-H 'Authorization: REDACTED'", "-d '{\"model\":\"gpt-4o\"}'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("curl missing %q in:\n%s", want, cmd)
		}
	}
}

func TestCurlCommandEscapesQuotes(t *testing.T) {
	r := &store.RequestLog{
		UpstreamURL:  "https://api.openai.com/v1/chat/completions",
		UpstreamBody: `{"content":"it's here"}`,
	}
	cmd := curlCommand(r)
	if !strings.Contains(cmd, `'\''`) {
		t.Errorf("single quote not escaped: %s", cmd)
	}
}

func TestCurlCommandNoUpstream(t *testing.T) {
	if got := curlCommand(&store.RequestLog{}); !strings.HasPrefix(got, "#") {
		t.Errorf("expected placeholder comment, got %q", got)
	}
}

func TestCurlCommandFallsBackToReqJSON(t *testing.T) {
	r := &store.RequestLog{
		UpstreamURL: "https://api.openai.com/v1/chat/completions",
		ReqJSON:     `{"from":"reqjson"}`,
	}
	cmd := curlCommand(r)
	if !strings.Contains(cmd, "reqjson") {
		t.Errorf("expected fallback to ReqJSON: %s", cmd)
	}
}
