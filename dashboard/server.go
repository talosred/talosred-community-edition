package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/talosred/ce/store"
)

// cacheInvalidator is satisfied by *metrics.CostCalculator — avoids import cycle.
type cacheInvalidator interface {
	InvalidateCache()
}

type Server struct {
	store       *store.Store
	broadcaster *store.Broadcaster
	costCalc    cacheInvalidator
	tmpl        *template.Template
	mux         *http.ServeMux
}

func NewServer(s *store.Store, b *store.Broadcaster, calc cacheInvalidator) *Server {
	srv := &Server{
		store:       s,
		broadcaster: b,
		costCalc:    calc,
	}
	srv.tmpl = template.Must(template.New("").Funcs(srv.funcMap()).ParseFS(tmplFS, "templates/*.html"))
	srv.mux = http.NewServeMux()
	srv.mux.HandleFunc("/ui", srv.handleLogs)
	srv.mux.HandleFunc("/ui/requests", srv.handleRequestList)
	srv.mux.HandleFunc("/ui/requests/", srv.handleRequestDetail)
	srv.mux.HandleFunc("/ui/settings", srv.handleSettings)
	srv.mux.HandleFunc("/ui/stream", srv.handleSSE)
	srv.mux.HandleFunc("/ui/pricing", srv.handlePricing)
	srv.mux.HandleFunc("/ui/pricing/", srv.handlePricingItem)
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
		"fmtPrice": func(f float64) string {
			return fmt.Sprintf("%.6f", f)
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
		"urlEncode": func(s string) string {
			return url.PathEscape(s)
		},
	}
}

// ---- Handlers: logs --------------------------------------------------------

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
	s.render(w, "logs.html", logsData{Page: "logs", Port: port(), Filter: f, Logs: logs})
}

func (s *Server) handleRequestList(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	logs, err := s.store.List(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "rows", logsData{Filter: f, Logs: logs})
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

// ---- Handler: settings -----------------------------------------------------

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
	s.render(w, "settings.html", settingsData{Page: "settings", Port: port(), EnvKeys: keys})
}

// ---- Handlers: pricing -----------------------------------------------------

type pricingData struct {
	Page    string
	Port    string
	Pricing []*store.ModelPricing
}

func (s *Server) handlePricing(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.pricingList(w, r, true)
	case http.MethodPost:
		s.pricingUpsert(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) pricingList(w http.ResponseWriter, _ *http.Request, fullPage bool) {
	rows, err := s.store.ListPricing()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := pricingData{Page: "pricing", Port: port(), Pricing: rows}
	if fullPage {
		s.render(w, "pricing.html", data)
	} else {
		s.renderPartial(w, "pricing-rows", data)
	}
}

func (s *Server) pricingUpsert(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		http.Error(w, "model required", http.StatusBadRequest)
		return
	}
	inputPer, err := strconv.ParseFloat(r.FormValue("input_per_1k"), 64)
	if err != nil {
		http.Error(w, "invalid input_per_1k", http.StatusBadRequest)
		return
	}
	outputPer, err := strconv.ParseFloat(r.FormValue("output_per_1k"), 64)
	if err != nil {
		http.Error(w, "invalid output_per_1k", http.StatusBadRequest)
		return
	}
	p := &store.ModelPricing{
		Model:       model,
		Provider:    r.FormValue("provider"),
		InputPer1k:  inputPer,
		OutputPer1k: outputPer,
	}
	if err := s.store.UpsertPricing(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.costCalc != nil {
		s.costCalc.InvalidateCache()
	}

	// Check if request wants single-row response (edit form) or full table (add form).
	// The edit form targets the specific row; the add form targets #pricing-rows.
	hxTarget := r.Header.Get("HX-Target")
	if strings.HasPrefix(hxTarget, "pricing-row-") {
		s.renderPartial(w, "pricing-row", p)
	} else {
		s.pricingList(w, r, false)
	}
}

// handlePricingItem handles /ui/pricing/{model} and /ui/pricing/{model}/edit|cancel
func (s *Server) handlePricingItem(w http.ResponseWriter, r *http.Request) {
	// path: /ui/pricing/{model}  or  /ui/pricing/{model}/edit  or /ui/pricing/{model}/cancel
	rest := strings.TrimPrefix(r.URL.Path, "/ui/pricing/")
	parts := strings.SplitN(rest, "/", 2)
	modelEnc := parts[0]
	modelName, err := url.PathUnescape(modelEnc)
	if err != nil {
		http.Error(w, "invalid model", http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "edit":
		p, err := s.store.GetPricing(modelName)
		if err != nil || p == nil {
			http.NotFound(w, r)
			return
		}
		s.renderPartial(w, "pricing-edit-row", p)

	case "cancel":
		p, err := s.store.GetPricing(modelName)
		if err != nil || p == nil {
			http.NotFound(w, r)
			return
		}
		s.renderPartial(w, "pricing-row", p)

	case "":
		if r.Method == http.MethodDelete {
			if err := s.store.DeletePricing(modelName); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if s.costCalc != nil {
				s.costCalc.InvalidateCache()
			}
			w.WriteHeader(http.StatusOK) // empty response removes the row via outerHTML swap
		} else {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

	default:
		http.NotFound(w, r)
	}
}

// ---- Handler: SSE ----------------------------------------------------------

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
	if p := os.Getenv("TALOSRED_PORT"); p != "" {
		return p
	}
	return "8080"
}

func sseEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", "&#10;")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
