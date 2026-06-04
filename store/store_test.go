package store_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/talosred/ce/store"
)

func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestInsertAndGet(t *testing.T) {
	db := setupDB(t)
	s := store.New(db)

	r := &store.RequestLog{
		ID:        "test-id-1",
		TS:        time.Now().Truncate(time.Millisecond),
		Provider:  "anthropic",
		Model:     "claude-3-5-sonnet-20241022",
		InputTok:  100,
		OutputTok: 50,
		TTFTms:    200,
		LatencyMs: 800,
		CostUSD:   0.00075,
		ReqJSON:   `{"model":"claude-3-5-sonnet-20241022"}`,
		ResJSON:   `{"id":"msg_1"}`,
	}

	if err := s.Insert(r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.Get("test-id-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}

	if got.ID != r.ID {
		t.Errorf("ID: got %q want %q", got.ID, r.ID)
	}
	if got.Provider != r.Provider {
		t.Errorf("Provider: got %q want %q", got.Provider, r.Provider)
	}
	if got.InputTok != r.InputTok {
		t.Errorf("InputTok: got %d want %d", got.InputTok, r.InputTok)
	}
	if got.CostUSD != r.CostUSD {
		t.Errorf("CostUSD: got %v want %v", got.CostUSD, r.CostUSD)
	}
}

func TestGetMissing(t *testing.T) {
	db := setupDB(t)
	s := store.New(db)

	got, err := s.Get("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestListEmpty(t *testing.T) {
	db := setupDB(t)
	s := store.New(db)

	logs, err := s.List(store.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(logs) != 0 {
		t.Errorf("expected 0 results, got %d", len(logs))
	}
}

func TestListFilterByProvider(t *testing.T) {
	db := setupDB(t)
	s := store.New(db)

	records := []*store.RequestLog{
		{ID: "a1", TS: time.Now(), Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"},
		{ID: "a2", TS: time.Now(), Provider: "anthropic", Model: "claude-3-haiku-20240307"},
		{ID: "g1", TS: time.Now(), Provider: "gemini", Model: "gemini-1.5-pro"},
		{ID: "o1", TS: time.Now(), Provider: "openai", Model: "gpt-4o"},
	}
	for _, r := range records {
		if err := s.Insert(r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}

	anthropicLogs, err := s.List(store.ListFilter{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(anthropicLogs) != 2 {
		t.Errorf("expected 2 anthropic records, got %d", len(anthropicLogs))
	}

	geminiLogs, err := s.List(store.ListFilter{Provider: "gemini"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(geminiLogs) != 1 {
		t.Errorf("expected 1 gemini record, got %d", len(geminiLogs))
	}
}

func TestListFilterByModel(t *testing.T) {
	db := setupDB(t)
	s := store.New(db)

	records := []*store.RequestLog{
		{ID: "1", TS: time.Now(), Provider: "openai", Model: "gpt-4o"},
		{ID: "2", TS: time.Now(), Provider: "openai", Model: "gpt-4o"},
		{ID: "3", TS: time.Now(), Provider: "openai", Model: "gpt-4o-mini"},
	}
	for _, r := range records {
		if err := s.Insert(r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	logs, err := s.List(store.ListFilter{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(logs) != 2 {
		t.Errorf("expected 2, got %d", len(logs))
	}
}

func TestListOrdering(t *testing.T) {
	db := setupDB(t)
	s := store.New(db)

	base := time.Now()
	records := []*store.RequestLog{
		{ID: "old", TS: base.Add(-1 * time.Hour), Provider: "openai", Model: "gpt-4o"},
		{ID: "new", TS: base, Provider: "openai", Model: "gpt-4o"},
	}
	for _, r := range records {
		if err := s.Insert(r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	logs, err := s.List(store.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2, got %d", len(logs))
	}
	// most recent first
	if logs[0].ID != "new" {
		t.Errorf("expected newest first, got %q", logs[0].ID)
	}
}

func TestListLimit(t *testing.T) {
	db := setupDB(t)
	s := store.New(db)

	for i := range 5 {
		r := &store.RequestLog{
			ID:       "id-" + string(rune('a'+i)),
			TS:       time.Now(),
			Provider: "openai",
			Model:    "gpt-4o",
		}
		if err := s.Insert(r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	logs, err := s.List(store.ListFilter{Limit: 3})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(logs) != 3 {
		t.Errorf("expected 3, got %d", len(logs))
	}
}
