package store

import (
	"database/sql"
	"fmt"
	"time"
)

type ModelPricing struct {
	Model       string
	Provider    string
	InputPer1k  float64
	OutputPer1k float64
	UpdatedAt   time.Time
}

func (s *Store) ListPricing() ([]*ModelPricing, error) {
	rows, err := s.db.Query(`
		SELECT model, provider, input_per_1k, output_per_1k, updated_at
		FROM model_pricing ORDER BY provider, model
	`)
	if err != nil {
		return nil, fmt.Errorf("list pricing: %w", err)
	}
	defer rows.Close()

	var out []*ModelPricing
	for rows.Next() {
		var p ModelPricing
		var tsMs int64
		if err := rows.Scan(&p.Model, &p.Provider, &p.InputPer1k, &p.OutputPer1k, &tsMs); err != nil {
			return nil, fmt.Errorf("scan pricing row: %w", err)
		}
		if tsMs > 0 {
			p.UpdatedAt = time.UnixMilli(tsMs)
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (s *Store) GetPricing(model string) (*ModelPricing, error) {
	var p ModelPricing
	var tsMs int64
	err := s.db.QueryRow(`
		SELECT model, provider, input_per_1k, output_per_1k, updated_at
		FROM model_pricing WHERE model = ?`, model).
		Scan(&p.Model, &p.Provider, &p.InputPer1k, &p.OutputPer1k, &tsMs)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pricing: %w", err)
	}
	if tsMs > 0 {
		p.UpdatedAt = time.UnixMilli(tsMs)
	}
	return &p, nil
}

func (s *Store) UpsertPricing(p *ModelPricing) error {
	_, err := s.db.Exec(`
		INSERT INTO model_pricing (model, provider, input_per_1k, output_per_1k, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			provider      = excluded.provider,
			input_per_1k  = excluded.input_per_1k,
			output_per_1k = excluded.output_per_1k,
			updated_at    = excluded.updated_at
	`, p.Model, p.Provider, p.InputPer1k, p.OutputPer1k, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("upsert pricing: %w", err)
	}
	return nil
}

func (s *Store) DeletePricing(model string) error {
	_, err := s.db.Exec(`DELETE FROM model_pricing WHERE model = ?`, model)
	if err != nil {
		return fmt.Errorf("delete pricing: %w", err)
	}
	return nil
}
