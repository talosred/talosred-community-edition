package store_test

import (
	"testing"

	"github.com/talosred/ce/store"
)

func TestSeedPricingOnMigrate(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	rows, err := s.ListPricing()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected seeded pricing rows, got 0")
	}
}

func TestGetPricingExact(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	p, err := s.GetPricing("gpt-4o")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p == nil {
		t.Fatal("expected gpt-4o pricing, got nil")
	}
	if p.Provider != "openai" {
		t.Errorf("provider: got %q want openai", p.Provider)
	}
	if p.InputPer1k != 0.0025 {
		t.Errorf("input_per_1k: got %v want 0.0025", p.InputPer1k)
	}
	if p.OutputPer1k != 0.010 {
		t.Errorf("output_per_1k: got %v want 0.010", p.OutputPer1k)
	}
}

func TestGetPricingMissing(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	p, err := s.GetPricing("does-not-exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil, got %+v", p)
	}
}

func TestUpsertPricingInsert(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	if err := s.UpsertPricing(&store.ModelPricing{
		Model:       "test-llm-1",
		Provider:    "openai",
		InputPer1k:  0.001,
		OutputPer1k: 0.002,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	p, err := s.GetPricing("test-llm-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p == nil {
		t.Fatal("expected row after upsert")
	}
	if p.InputPer1k != 0.001 {
		t.Errorf("input_per_1k: got %v want 0.001", p.InputPer1k)
	}
}

func TestUpsertPricingUpdate(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	// update seeded gpt-4o price
	if err := s.UpsertPricing(&store.ModelPricing{
		Model:       "gpt-4o",
		Provider:    "openai",
		InputPer1k:  0.999,
		OutputPer1k: 1.999,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	p, err := s.GetPricing("gpt-4o")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.InputPer1k != 0.999 {
		t.Errorf("input_per_1k after update: got %v want 0.999", p.InputPer1k)
	}
	if p.OutputPer1k != 1.999 {
		t.Errorf("output_per_1k after update: got %v want 1.999", p.OutputPer1k)
	}
}

func TestDeletePricing(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	if err := s.DeletePricing("gpt-4o"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	p, err := s.GetPricing("gpt-4o")
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if p != nil {
		t.Errorf("expected nil after delete, got %+v", p)
	}
}

func TestDeletePricingNonExistent(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	// should not error on missing row
	if err := s.DeletePricing("never-existed"); err != nil {
		t.Errorf("delete non-existent: unexpected error: %v", err)
	}
}

func TestListPricingOrdering(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	rows, err := s.ListPricing()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// all seeded anthropic rows should come before openai
	var lastProvider string
	for _, r := range rows {
		if lastProvider != "" && r.Provider < lastProvider {
			t.Errorf("not sorted by provider: %q after %q", r.Provider, lastProvider)
		}
		lastProvider = r.Provider
	}
}

func TestSeedIsIdempotent(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	before, _ := s.ListPricing()

	// migrate again — seed uses INSERT OR IGNORE, must not add duplicates
	if err := store.Migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	after, _ := s.ListPricing()
	if len(before) != len(after) {
		t.Errorf("seed not idempotent: before %d rows, after %d", len(before), len(after))
	}
}
