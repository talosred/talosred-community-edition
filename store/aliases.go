package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type ModelAlias struct {
	Pattern     string // exact model name, or prefix ending in "*"
	TargetModel string
	TargetURL   string // upstream base URL ("" = provider default)
	Provider    string // openai | anthropic | gemini
	Enabled     bool
	UpdatedAt   time.Time
}

func (s *Store) ListAliases() ([]*ModelAlias, error) {
	rows, err := s.db.Query(`
		SELECT pattern, target_model, target_url, provider, enabled, updated_at
		FROM model_aliases ORDER BY pattern`)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()

	var out []*ModelAlias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAlias(pattern string) (*ModelAlias, error) {
	row := s.db.QueryRow(`
		SELECT pattern, target_model, target_url, provider, enabled, updated_at
		FROM model_aliases WHERE pattern = ?`, pattern)
	a, err := scanAlias(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get alias: %w", err)
	}
	return a, nil
}

// MatchAlias resolves a requested model to an enabled alias. Exact match wins;
// otherwise the longest matching "prefix*" pattern is used.
func (s *Store) MatchAlias(model string) (*ModelAlias, error) {
	aliases, err := s.ListAliases()
	if err != nil {
		return nil, err
	}

	var best *ModelAlias
	for _, a := range aliases {
		if !a.Enabled {
			continue
		}
		if a.Pattern == model {
			return a, nil // exact match — highest priority
		}
		if prefix, ok := strings.CutSuffix(a.Pattern, "*"); ok {
			if strings.HasPrefix(model, prefix) {
				if best == nil || len(a.Pattern) > len(best.Pattern) {
					best = a
				}
			}
		}
	}
	return best, nil
}

func (s *Store) UpsertAlias(a *ModelAlias) error {
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO model_aliases (pattern, target_model, target_url, provider, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(pattern) DO UPDATE SET
			target_model = excluded.target_model,
			target_url   = excluded.target_url,
			provider     = excluded.provider,
			enabled      = excluded.enabled,
			updated_at   = excluded.updated_at`,
		a.Pattern, a.TargetModel, a.TargetURL, a.Provider, enabled, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("upsert alias: %w", err)
	}
	return nil
}

func (s *Store) DeleteAlias(pattern string) error {
	_, err := s.db.Exec(`DELETE FROM model_aliases WHERE pattern = ?`, pattern)
	if err != nil {
		return fmt.Errorf("delete alias: %w", err)
	}
	return nil
}

func scanAlias(sc interface{ Scan(...any) error }) (*ModelAlias, error) {
	var a ModelAlias
	var enabled int
	var tsMs int64
	if err := sc.Scan(&a.Pattern, &a.TargetModel, &a.TargetURL, &a.Provider, &enabled, &tsMs); err != nil {
		return nil, err
	}
	a.Enabled = enabled != 0
	if tsMs > 0 {
		a.UpdatedAt = time.UnixMilli(tsMs)
	}
	return &a, nil
}
