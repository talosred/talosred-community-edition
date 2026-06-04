package store_test

import (
	"testing"
	"time"

	"github.com/talosred/ce/store"
)

func seedAttributionData(t *testing.T, s *store.Store) {
	t.Helper()
	rows := []*store.RequestLog{
		{ID: "1", TS: time.Now(), Provider: "openai", Model: "gpt-4o", AppName: "billing", UserName: "dave", InputTok: 100, OutputTok: 50, CostUSD: 0.01, LatencyMs: 200},
		{ID: "2", TS: time.Now(), Provider: "openai", Model: "gpt-4o", AppName: "billing", UserName: "dave", InputTok: 200, OutputTok: 80, CostUSD: 0.02, LatencyMs: 400},
		{ID: "3", TS: time.Now(), Provider: "openai", Model: "gpt-4o", AppName: "search", UserName: "alice", InputTok: 50, OutputTok: 20, CostUSD: 0.005, LatencyMs: 100},
		{ID: "4", TS: time.Now(), Provider: "openai", Model: "gpt-4o", InputTok: 10, OutputTok: 5, CostUSD: 0.001, LatencyMs: 50}, // unattributed
	}
	for _, r := range rows {
		if err := s.Insert(r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}
}

func TestAttributionByApp(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	seedAttributionData(t, s)

	rows, err := s.AttributionByApp()
	if err != nil {
		t.Fatalf("attribution: %v", err)
	}
	// billing, search, unattributed("")
	if len(rows) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(rows))
	}

	byKey := map[string]*store.AttributionRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}

	billing := byKey["billing"]
	if billing == nil {
		t.Fatal("missing billing group")
	}
	if billing.Requests != 2 {
		t.Errorf("billing requests: got %d want 2", billing.Requests)
	}
	if billing.InputTok != 300 {
		t.Errorf("billing input tok: got %d want 300", billing.InputTok)
	}
	wantCost := 0.03
	if billing.CostUSD < wantCost-1e-9 || billing.CostUSD > wantCost+1e-9 {
		t.Errorf("billing cost: got %v want %v", billing.CostUSD, wantCost)
	}
	if billing.AvgLatMs != 300 {
		t.Errorf("billing avg latency: got %d want 300", billing.AvgLatMs)
	}
}

func TestAttributionByUser(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	seedAttributionData(t, s)

	rows, err := s.AttributionByUser()
	if err != nil {
		t.Fatalf("attribution: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 user groups, got %d", len(rows))
	}

	// sorted by cost DESC — dave (0.03) should be first
	if rows[0].Key != "dave" {
		t.Errorf("expected dave first (highest cost), got %q", rows[0].Key)
	}
}

func TestAttributionEmpty(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	rows, err := s.AttributionByApp()
	if err != nil {
		t.Fatalf("attribution: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestListFilterByAppAndUser(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	seedAttributionData(t, s)

	billing, err := s.List(store.ListFilter{App: "billing"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(billing) != 2 {
		t.Errorf("app=billing: got %d want 2", len(billing))
	}

	alice, err := s.List(store.ListFilter{User: "alice"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(alice) != 1 {
		t.Errorf("user=alice: got %d want 1", len(alice))
	}
}

func TestRequestLogRoundTripsNewFields(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	in := &store.RequestLog{
		ID: "rt-1", TS: time.Now(), Provider: "openai", Model: "gpt-4o",
		AppName: "billing", UserName: "dave",
		StatusCode: 429, Retries: 2,
		UpstreamURL:     "https://api.openai.com/v1/chat/completions",
		UpstreamHeaders: `{"Authorization":"REDACTED"}`,
		UpstreamBody:    `{"model":"gpt-4o"}`,
	}
	if err := s.Insert(in); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.Get("rt-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AppName != "billing" || got.UserName != "dave" {
		t.Errorf("attribution lost: %+v", got)
	}
	if got.StatusCode != 429 || got.Retries != 2 {
		t.Errorf("status/retries lost: %d/%d", got.StatusCode, got.Retries)
	}
	if got.UpstreamURL == "" || got.UpstreamBody == "" {
		t.Errorf("upstream fields lost: %+v", got)
	}
}
