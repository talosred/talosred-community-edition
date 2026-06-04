package metrics_test

import (
	"math"
	"testing"

	"github.com/talosred/ce/metrics"
	"github.com/talosred/ce/store"
)

func setupCalc(t *testing.T) *metrics.CostCalculator {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s := store.New(db, nil)
	return metrics.NewCostCalculator(s)
}

func approxEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestKnownModelExact(t *testing.T) {
	c := setupCalc(t)
	// gpt-4o: $0.0025/1k input, $0.010/1k output
	// 1000 in + 500 out = 0.0025 + 0.005 = 0.0075
	cost := c.Calculate("gpt-4o", 1000, 500)
	if !approxEqual(cost, 0.0075, 1e-9) {
		t.Errorf("gpt-4o: got %v want 0.0075", cost)
	}
}

func TestClaudeModel(t *testing.T) {
	c := setupCalc(t)
	// claude-3-5-sonnet-20241022: $0.003/1k in, $0.015/1k out
	// 2000 in + 1000 out = 0.006 + 0.015 = 0.021
	cost := c.Calculate("claude-3-5-sonnet-20241022", 2000, 1000)
	if !approxEqual(cost, 0.021, 1e-9) {
		t.Errorf("claude: got %v want 0.021", cost)
	}
}

func TestGeminiModel(t *testing.T) {
	c := setupCalc(t)
	// gemini-1.5-pro: $0.00125/1k in, $0.005/1k out
	cost := c.Calculate("gemini-1.5-pro", 4000, 2000)
	want := (4000.0/1000)*0.00125 + (2000.0/1000)*0.005
	if !approxEqual(cost, want, 1e-9) {
		t.Errorf("gemini: got %v want %v", cost, want)
	}
}

func TestPrefixMatch(t *testing.T) {
	c := setupCalc(t)
	// "gpt-4o-2024-08-06" should match "gpt-4o" prefix
	cost := c.Calculate("gpt-4o-2024-08-06", 1000, 0)
	if cost == 0 {
		t.Error("expected non-zero cost for gpt-4o variant via prefix match")
	}
}

func TestUnknownModelReturnsZero(t *testing.T) {
	c := setupCalc(t)
	cost := c.Calculate("unknown-model-xyz", 1000, 1000)
	if cost != 0 {
		t.Errorf("expected 0 for unknown model, got %v", cost)
	}
}

func TestZeroTokens(t *testing.T) {
	c := setupCalc(t)
	cost := c.Calculate("gpt-4o", 0, 0)
	if cost != 0 {
		t.Errorf("expected 0 cost for 0 tokens, got %v", cost)
	}
}

func TestGPT4TurboPrice(t *testing.T) {
	c := setupCalc(t)
	// gpt-4-turbo: $0.010/1k in, $0.030/1k out
	cost := c.Calculate("gpt-4-turbo", 1000, 1000)
	want := 0.010 + 0.030
	if !approxEqual(cost, want, 1e-9) {
		t.Errorf("gpt-4-turbo: got %v want %v", cost, want)
	}
}

func TestInvalidateCacheReloadsFromDB(t *testing.T) {
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s := store.New(db, nil)
	c := metrics.NewCostCalculator(s)

	// initially unknown model → 0
	if cost := c.Calculate("test-model-x", 1000, 0); cost != 0 {
		t.Fatalf("expected 0 before upsert, got %v", cost)
	}

	// add pricing
	if err := s.UpsertPricing(&store.ModelPricing{
		Model: "test-model-x", Provider: "openai",
		InputPer1k: 0.001, OutputPer1k: 0.002,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// still cached → still 0
	if cost := c.Calculate("test-model-x", 1000, 0); cost != 0 {
		t.Fatalf("expected 0 from cache, got %v", cost)
	}

	// invalidate → picks up new row
	c.InvalidateCache()
	cost := c.Calculate("test-model-x", 1000, 0)
	want := 0.001
	if !approxEqual(cost, want, 1e-9) {
		t.Errorf("after invalidate: got %v want %v", cost, want)
	}
}
