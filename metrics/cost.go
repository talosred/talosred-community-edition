package metrics

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed pricing.json
var pricingJSON []byte

type modelPricing struct {
	InputPer1k  float64 `json:"input_per_1k"`
	OutputPer1k float64 `json:"output_per_1k"`
}

type pricingFile struct {
	Models map[string]modelPricing `json:"models"`
}

type CostCalculator struct {
	models map[string]modelPricing
}

func NewCostCalculator() (*CostCalculator, error) {
	var pf pricingFile
	if err := json.Unmarshal(pricingJSON, &pf); err != nil {
		return nil, fmt.Errorf("parse pricing.json: %w", err)
	}
	return &CostCalculator{models: pf.Models}, nil
}

// Calculate returns cost in USD. Falls back to prefix match for model variants.
func (c *CostCalculator) Calculate(model string, inputTok, outputTok int64) float64 {
	p, ok := c.models[model]
	if !ok {
		// prefix match: "gpt-4o-2024-08-06" → "gpt-4o"
		for k, v := range c.models {
			if strings.HasPrefix(model, k) {
				p = v
				ok = true
				break
			}
		}
	}
	if !ok {
		return 0
	}
	return (float64(inputTok)/1000)*p.InputPer1k + (float64(outputTok)/1000)*p.OutputPer1k
}

// KnownModels returns all models with pricing data.
func (c *CostCalculator) KnownModels() []string {
	out := make([]string, 0, len(c.models))
	for k := range c.models {
		out = append(out, k)
	}
	return out
}
