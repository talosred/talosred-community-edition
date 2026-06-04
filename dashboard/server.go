package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/talosred/ce/store"
)

type Server struct {
	store       *store.Store
	broadcaster *store.Broadcaster
	tmpl        *template.Template
	mux         *http.ServeMux
}

func NewServer(s *store.Store, b *store.Broadcaster) *Server {
	srv := &Server{
		store:       s,
		broadcaster: b,
	}
	srv.tmpl = template.Must(template.New("").Funcs(srv.funcMap()).ParseFS(tmplFS, "templates/*.html"))
	srv.mux = http.NewServeMux()
	srv.mux.HandleFunc("/ui", srv.handleLogs)
	srv.mux.HandleFunc("/ui/requests", srv.handleRequestList)
	srv.mux.HandleFunc("/ui/requests/", srv.handleRequestDetail)
	srv.mux.HandleFunc("/ui/settings", srv.handleSettings)
	srv.mux.HandleFunc("/ui/stream", srv.handleSSE)
	srv.mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	return srv
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// ---- Template funcs --------------------------------------------------------

func (s *Server) funcMap() template.FuncMap {
	return template.FuncMap{
		"fmtTime": func(t time.Time) string {
			return t.Format("15:04:05")
		},
		"fmtTimeFull": func(t time.Time) string {
			return t.Format("2006-01-02 15:04:05")
		},
		"fmtCost": func(f float64) string {
			if f < 0.0001 {
				return fmt.Sprintf("%.6f", f)
			}
			return fmt.Sprintf("%.4f", f)
		},
		"fmtNum": func(n int64) string {
			if n >= 1000 {
				return fmt.Sprintf("%dk", n/1000)
			}
			return fmt.Sprintf("%d", n)
		},
		"truncate": func(n int, s string) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
		"prettyJSON": func(raw string) template.HTML {
			if raw == "" {
				return "—"
			}
			var buf bytes.Buffer
			if err := json.Indent(&buf, []byte(raw), "", "  "); err != nil {
				return template.HTML(template.HTMLEscapeString(raw))
			}
			return template.HTML(template.HTMLEscapeString(buf.String()))
		},
		"not": func(v any) bool {
			if v == nil {
				return true
			}
			switch x := v.(type) {
			case bool:
				return !x
			case int:
				return x == 0
			case string:
				return x == ""
			}
			return false
		},
	}
}

// ---- Handlers --------------------------------------------------------------

type logsData struct {
	Page   string
	Port   string
	Filter store.ListFilter
	Logs   []*store.RequestLog
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	logs, err := s.store.List(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := logsData{
		Page:   "logs",
		Port:   port(),
		Filter: f,
		Logs:   logs,
	}
	s.render(w, "logs.html", data)
}

func (s *Server) handleRequestList(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	logs, err := s.store.List(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := logsData{Filter: f, Logs: logs}
	s.renderPartial(w, "rows", data)
}

func (s *Server) handleRequestDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/ui/requests/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	entry, err := s.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	s.renderPartial(w, "detail", entry)
}

type envKey struct {
	Name     string
	Provider string
	Set      bool
}

type settingsData struct {
	Page    string
	Port    string
	EnvKeys []envKey
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	keys := []envKey{
		{Name: "OPENAI_API_KEY", Provider: "OpenAI", Set: os.Getenv("OPENAI_API_KEY") != ""},
		{Name: "ANTHROPIC_API_KEY", Provider: "Anthropic", Set: os.Getenv("ANTHROPIC_API_KEY") != ""},
		{Name: "GEMINI_API_KEY", Provider: "Gemini", Set: os.Getenv("GEMINI_API_KEY") != ""},
		{Name: "GOOGLE_API_KEY", Provider: "Gemini (alt)", Set: os.Getenv("GOOGLE_API_KEY") != ""},
	}
	data := settingsData{Page: "settings", Port: port(), EnvKeys: keys}
	s.render(w, "settings.html", data)
}

// handleSSE streams new RequestLog rows to connected dashboard clients via
// Server-Sent Events. Each event is an HTML fragment (a <tr>) ready for HTMX
// to prepend into #log-table-body.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	ch, cancel := s.broadcaster.Subscribe()
	defer cancel()

	// keepalive ticker so proxies don't drop idle connections
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()

		case entry, ok := <-ch:
			if !ok {
				return
			}
			var buf bytes.Buffer
			if err := s.tmpl.ExecuteTemplate(&buf, "row", entry); err != nil {
				log.Printf("sse render row: %v", err)
				continue
			}
			// SSE format: data lines only (HTMX sse-swap reads "message" events)
			fmt.Fprintf(w, "data: %s\n\n", sseEscape(buf.String()))
			flusher.Flush()
		}
	}
}

// ---- Helpers ---------------------------------------------------------------

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render partial %s: %v", name, err)
	}
}

func parseFilter(r *http.Request) store.ListFilter {
	return store.ListFilter{
		Provider: r.URL.Query().Get("provider"),
		Model:    r.URL.Query().Get("model"),
		Limit:    100,
	}
}

func port() string {
	// best-effort: read from env set by main, fallback to 8080
	if p := os.Getenv("TALOSRED_PORT"); p != "" {
		return p
	}
	return "8080"
}

// sseEscape replaces newlines in HTML fragments so they fit on one SSE data line.
// HTMX reconstructs the HTML from the single line correctly.
func sseEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", "&#10;")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
