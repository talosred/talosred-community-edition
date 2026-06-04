package store_test

import (
	"testing"

	"github.com/talosred/ce/store"
)

func TestAliasUpsertAndGet(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)

	if err := s.UpsertAlias(&store.ModelAlias{
		Pattern: "gpt-3.5-turbo", TargetModel: "llama3.2",
		TargetURL: "http://localhost:11434", Provider: "openai", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	a, err := s.GetAlias("gpt-3.5-turbo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if a == nil {
		t.Fatal("expected alias")
	}
	if a.TargetModel != "llama3.2" || a.TargetURL != "http://localhost:11434" || !a.Enabled {
		t.Errorf("unexpected alias: %+v", a)
	}
}

func TestAliasGetMissing(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	a, err := s.GetAlias("nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if a != nil {
		t.Errorf("expected nil, got %+v", a)
	}
}

func TestAliasDelete(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	s.UpsertAlias(&store.ModelAlias{Pattern: "x", TargetModel: "y", Provider: "openai", Enabled: true})
	if err := s.DeleteAlias("x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	a, _ := s.GetAlias("x")
	if a != nil {
		t.Error("expected nil after delete")
	}
}

func TestMatchAliasExact(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-3.5-turbo", TargetModel: "llama3.2", Provider: "openai", Enabled: true})

	a, err := s.MatchAlias("gpt-3.5-turbo")
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if a == nil || a.TargetModel != "llama3.2" {
		t.Errorf("exact match failed: %+v", a)
	}
}

func TestMatchAliasPrefix(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-3*", TargetModel: "llama3.2", Provider: "openai", Enabled: true})

	a, err := s.MatchAlias("gpt-3.5-turbo-16k")
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if a == nil || a.TargetModel != "llama3.2" {
		t.Errorf("prefix match failed: %+v", a)
	}
}

func TestMatchAliasLongestPrefixWins(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt*", TargetModel: "broad", Provider: "openai", Enabled: true})
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-4*", TargetModel: "specific", Provider: "openai", Enabled: true})

	a, err := s.MatchAlias("gpt-4o")
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if a == nil || a.TargetModel != "specific" {
		t.Errorf("expected longest prefix 'specific', got %+v", a)
	}
}

func TestMatchAliasExactBeatsPrefix(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-4*", TargetModel: "prefix", Provider: "openai", Enabled: true})
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-4o", TargetModel: "exact", Provider: "openai", Enabled: true})

	a, _ := s.MatchAlias("gpt-4o")
	if a == nil || a.TargetModel != "exact" {
		t.Errorf("expected exact match to win, got %+v", a)
	}
}

func TestMatchAliasSkipsDisabled(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	s.UpsertAlias(&store.ModelAlias{Pattern: "gpt-3.5-turbo", TargetModel: "llama3.2", Provider: "openai", Enabled: false})

	a, err := s.MatchAlias("gpt-3.5-turbo")
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if a != nil {
		t.Errorf("disabled alias should not match, got %+v", a)
	}
}

func TestMatchAliasNoMatch(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	a, err := s.MatchAlias("claude-3-5-sonnet-20241022")
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if a != nil {
		t.Errorf("expected no match, got %+v", a)
	}
}

func TestAliasToggleViaUpsert(t *testing.T) {
	db := setupDB(t)
	s := store.New(db, nil)
	s.UpsertAlias(&store.ModelAlias{Pattern: "x", TargetModel: "y", Provider: "openai", Enabled: true})

	a, _ := s.GetAlias("x")
	a.Enabled = false
	if err := s.UpsertAlias(a); err != nil {
		t.Fatalf("toggle upsert: %v", err)
	}
	got, _ := s.GetAlias("x")
	if got.Enabled {
		t.Error("expected disabled after toggle")
	}
}
