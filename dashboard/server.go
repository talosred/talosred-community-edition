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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/talosred/ce/hooks"
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
	hooks       *hooks.Runner
	tmpl        *template.Template
	mux         *http.ServeMux
}

func NewServer(s *store.Store, b *store.Broadcaster, calc cacheInvalidator, hr *hooks.Runner) *Server {
	srv := &Server{
		store:       s,
		broadcaster: b,
		costCalc:    calc,
		hooks:       hr,
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
	srv.mux.HandleFunc("/ui/hooks", srv.handleHooks)
	srv.mux.HandleFunc("/ui/hooks/", srv.handleHookToggle)
	srv.mux.HandleFunc("/ui/usage", srv.handleUsage)
	srv.mux.HandleFunc("/ui/aliases", srv.handleAliases)
	srv.mux.HandleFunc("/ui/aliases/", srv.handleAliasItem)
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
		"fmtDuration": func(d time.Duration) string {
			if d < time.Millisecond {
				return fmt.Sprintf("%dµs", d.Microseconds())
			}
			return fmt.Sprintf("%dms", d.Milliseconds())
		},
		"statusClass": func(code int) string {
			switch {
			case code == 0:
				return ""
			case code >= 200 && code < 300:
				return "status-ok"
			case code == 429:
				return "status-warn"
			default:
				return "status-err"
			}
		},
		"curlCommand": curlCommand,
	}
}

// curlCommand reconstructs the exact upstream call as a runnable curl command
// (Pain 3). Secrets are already redacted in the stored headers/url.
func curlCommand(r *store.RequestLog) string {
	if r.UpstreamURL == "" {
		return "# upstream request not captured"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "curl -X POST '%s'", r.UpstreamURL)

	var headers map[string]string
	if r.UpstreamHeaders != "" {
		_ = json.Unmarshal([]byte(r.UpstreamHeaders), &headers)
	}
	// stable header order for reproducible output
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " \\\n  -H '%s: %s'", k, headers[k])
	}

	body := r.UpstreamBody
	if body == "" {
		body = r.ReqJSON
	}
	if body != "" {
		// escape single quotes for safe single-quoted shell string
		body = strings.ReplaceAll(body, "'", `'\''`)
		fmt.Fprintf(&b, " \\\n  -d '%s'", body)
	}
	return b.String()
}

// ---- Handlers: logs --------------------------------------------------------

type logsData struct {
	Page       string
	Port       string
	Filter     store.ListFilter
	Logs       []*store.RequestLog
	HasMore    bool
	NextOffset int
}

func newLogsData(f store.ListFilter, logs []*store.RequestLog) logsData {
	return logsData{
		Filter:     f,
		Logs:       logs,
		HasMore:    len(logs) == f.Limit, // a full page implies there may be more
		NextOffset: f.Offset + f.Limit,
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	logs, err := s.store.List(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d := newLogsData(f, logs)
	d.Page = "logs"
	d.Port = port()
	s.render(w, "logs.html", d)
}

func (s *Server) handleRequestList(w http.ResponseWriter, r *http.Request) {
	f := parseFilter(r)
	logs, err := s.store.List(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderPartial(w, "rows-append", newLogsData(f, logs))
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

// ---- Handlers: hooks -------------------------------------------------------

type hooksData struct {
	Page     string
	HooksDir string
	Hooks    []hooks.HookInfo
}

func (s *Server) handleHooks(w http.ResponseWriter, _ *http.Request) {
	data := hooksData{
		Page:     "hooks",
		HooksDir: s.hooks.HooksDir(),
		Hooks:    s.hooks.ListHooks(),
	}
	s.render(w, "hooks.html", data)
}

// handleHookToggle handles POST /ui/hooks/{type}/{name}/toggle
func (s *Server) handleHookToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// path: /ui/hooks/{type}/{name}/toggle
	rest := strings.TrimPrefix(r.URL.Path, "/ui/hooks/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 || parts[2] != "toggle" {
		http.NotFound(w, r)
		return
	}
	hookType, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid hook type", http.StatusBadRequest)
		return
	}
	hookName, err := url.PathUnescape(parts[1])
	if err != nil {
		http.Error(w, "invalid hook name", http.StatusBadRequest)
		return
	}

	// determine current state and flip it
	allHooks := s.hooks.ListHooks()
	var target *hooks.HookInfo
	for i := range allHooks {
		if allHooks[i].Type == hookType && allHooks[i].Name == hookName {
			target = &allHooks[i]
			break
		}
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}

	if err := s.hooks.SetEnabled(hookType, hookName, !target.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// re-fetch and render the updated row
	updated := s.hooks.ListHooks()
	for i := range updated {
		if updated[i].Type == hookType && updated[i].Name == hookName {
			s.renderPartial(w, "hook-row", updated[i])
			return
		}
	}
	http.NotFound(w, r)
}

// ---- Handler: usage / attribution (Pain 1) ---------------------------------

type usageData struct {
	Page   string
	ByApp  []*store.AttributionRow
	ByUser []*store.AttributionRow
}

func (s *Server) handleUsage(w http.ResponseWriter, _ *http.Request) {
	byApp, err := s.store.AttributionByApp()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byUser, err := s.store.AttributionByUser()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "usage.html", usageData{Page: "usage", ByApp: byApp, ByUser: byUser})
}

// ---- Handlers: model aliases (Pain 5) --------------------------------------

type aliasesData struct {
	Page    string
	Aliases []*store.ModelAlias
}

func (s *Server) handleAliases(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.aliasList(w, true)
	case http.MethodPost:
		s.aliasUpsert(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) aliasList(w http.ResponseWriter, fullPage bool) {
	rows, err := s.store.ListAliases()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if fullPage {
		s.render(w, "aliases.html", aliasesData{Page: "aliases", Aliases: rows})
	} else {
		s.renderPartial(w, "alias-rows", aliasesData{Aliases: rows})
	}
}

func (s *Server) aliasUpsert(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pattern := strings.TrimSpace(r.FormValue("pattern"))
	target := strings.TrimSpace(r.FormValue("target_model"))
	if pattern == "" || target == "" {
		http.Error(w, "pattern and target_model required", http.StatusBadRequest)
		return
	}
	provider := r.FormValue("provider")
	if provider == "" {
		provider = "openai"
	}
	a := &store.ModelAlias{
		Pattern:     pattern,
		TargetModel: target,
		TargetURL:   strings.TrimSpace(r.FormValue("target_url")),
		Provider:    provider,
		Enabled:     r.FormValue("enabled") != "false",
	}
	if err := s.store.UpsertAlias(a); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.aliasList(w, false)
}

// handleAliasItem handles /ui/aliases/{pattern} (DELETE) and
// /ui/aliases/{pattern}/toggle (POST).
func (s *Server) handleAliasItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/ui/aliases/")
	parts := strings.SplitN(rest, "/", 2)
	pattern, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid pattern", http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "toggle":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a, err := s.store.GetAlias(pattern)
		if err != nil || a == nil {
			http.NotFound(w, r)
			return
		}
		a.Enabled = !a.Enabled
		if err := s.store.UpsertAlias(a); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.aliasList(w, false)

	case "":
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.store.DeleteAlias(pattern); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.aliasList(w, false)

	default:
		http.NotFound(w, r)
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
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	return store.ListFilter{
		Provider: r.URL.Query().Get("provider"),
		Model:    r.URL.Query().Get("model"),
		App:      r.URL.Query().Get("app"),
		User:     r.URL.Query().Get("user"),
		Limit:    100,
		Offset:   offset,
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
