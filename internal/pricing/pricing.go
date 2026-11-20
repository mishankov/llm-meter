package pricing

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"time"
)

// Rates are US dollars per million tokens.
type Rates struct {
	Input         float64 `json:"input"`
	CachedInput   float64 `json:"cachedInput"`
	Output        float64 `json:"output"`
	CacheWrite    float64 `json:"cacheWrite"`
	CacheWrite12h float64 `json:"cacheWrite12h"`
}

type Catalog struct {
	overrides map[string]Rates
}

// OpenAI's standard text rates. Models whose usage spans differently priced
// modalities are left unpriced unless the operator explicitly supplies rates.
var standard = map[string]Rates{
	"gpt-6.2-astra":          {Input: 8, CachedInput: .4, Output: 40, CacheWrite: 10},
	"gpt-6.2-sol":            {Input: 2, CachedInput: .1, Output: 10, CacheWrite: 2.5},
	"gpt-6-astra":            {Input: 10, CachedInput: 1, Output: 50, CacheWrite: 12.5},
	"gpt-6.1-sol":            {Input: 2, CachedInput: .1, Output: 10, CacheWrite: 2.5},
	"gpt-6.1-terra":          {Input: 1, CachedInput: .05, Output: 5, CacheWrite: 1.25},
	"gpt-6-luna":             {Input: .1, CachedInput: .01, Output: .5, CacheWrite: .125},
	"gpt-5.6-sol":            {Input: 4, CachedInput: .4, Output: 20, CacheWrite: 5},
	"gpt-5.5":                {Input: 5, CachedInput: .5, Output: 30},
	"gpt-5.4":                {Input: 2.5, CachedInput: .25, Output: 15},
	"gpt-5.4-mini":           {Input: .75, CachedInput: .075, Output: 4.5},
	"gpt-5.4-nano":           {Input: .20, CachedInput: .02, Output: 1.25},
	"gpt-5.3-codex":          {Input: 1.75, CachedInput: .175, Output: 14},
	"gpt-5.2-codex":          {Input: 1.75, CachedInput: .175, Output: 14},
	"gpt-5.2":                {Input: 1.75, CachedInput: .175, Output: 14},
	"gpt-5.1-codex":          {Input: 1.25, CachedInput: .125, Output: 10},
	"gpt-5":                  {Input: 1.25, CachedInput: .125, Output: 10},
	"gpt-5-mini":             {Input: .25, CachedInput: .025, Output: 2},
	"gpt-5-nano":             {Input: .05, CachedInput: .005, Output: .4},
	"gpt-4.1":                {Input: 2, CachedInput: .5, Output: 8},
	"gpt-4.1-mini":           {Input: .4, CachedInput: .1, Output: 1.6},
	"gpt-4.1-nano":           {Input: .1, CachedInput: .025, Output: .4},
	"text-embedding-3-small": {Input: .02},
	"text-embedding-3-large": {Input: .13},
	"text-embedding-ada-002": {Input: .1},
}

var snapshotSuffix = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

func Load(path string) (Catalog, error) {
	var catalog Catalog
	if path == "" {
		return catalog, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return catalog, fmt.Errorf("read rates file: %w", err)
	}
	if err := json.Unmarshal(contents, &catalog.overrides); err != nil {
		return catalog, fmt.Errorf("decode rates file: %w", err)
	}
	for name, rates := range catalog.overrides {
		if strings.TrimSpace(name) == "" || !rates.valid() {
			return catalog, fmt.Errorf("invalid rates for model %q", name)
		}
	}
	return catalog, nil
}

func (r Rates) valid() bool {
	for _, value := range []float64{r.Input, r.CachedInput, r.Output, r.CacheWrite, r.CacheWrite12h} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return true
}

func (c Catalog) ForUsage(model, tier string, at time.Time, input, cached, writes, writes12h, output int64) (*Rates, *float64) {
	if input < 0 || cached < 0 || writes < 0 || writes12h < 0 || writes12h > writes || output < 0 || cached > input || writes > input-cached {
		return nil, nil
	}
	rates, overridden := c.overrides[model]
	if !overridden {
		base := snapshotSuffix.ReplaceAllString(model, "")
		if base == "gpt-5.6" {
			base = "gpt-5.6-sol"
		}
		var ok bool
		rates, ok = standard[base]
		if !ok || (writes > 0 && rates.CacheWrite == 0) {
			return nil, nil
		}
		if base == "gpt-5.6-sol" && !at.Before(time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)) {
			return nil, nil // The published promotional rates expire at the end of November (Pacific time).
		}
		switch base {
		case "gpt-6.2-astra", "gpt-6.2-sol", "gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.5", "gpt-5.4":
			if input > 272_000 {
				rates.Input *= 2
				rates.CachedInput *= 2
				rates.CacheWrite *= 2
				rates.Output *= 1.5
			}
		}
		multiplier, ok := tierMultiplier(base, tier, at)
		if !ok {
			return nil, nil
		}
		rates.Input *= multiplier
		rates.CachedInput *= multiplier
		rates.Output *= multiplier
		rates.CacheWrite *= multiplier
		rates.CacheWrite12h = rates.CacheWrite * 2
	}
	cost := (float64(input-cached-writes)*rates.Input + float64(cached)*rates.CachedInput +
		float64(writes-writes12h)*rates.CacheWrite + float64(writes12h)*rates.CacheWrite12h + float64(output)*rates.Output) / 1_000_000
	return &rates, &cost
}

func tierMultiplier(model, tier string, at time.Time) (float64, bool) {
	switch tier {
	case "", "auto", "default", "standard":
		return 1, true
	case "batch", "flex":
		if strings.HasPrefix(model, "gpt-6") {
			return .5, true
		}
	case "fast", "priority":
		if model == "gpt-5.5" {
			return 2.5, true
		}
		if strings.HasPrefix(model, "gpt-6") || model == "gpt-5.6-sol" {
			return 2, true
		}
	case "ultrafast":
		switch model {
		case "gpt-6.2-astra", "gpt-6.2-sol":
			if at.Before(time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)) {
				return 3, true
			}
			return 6, true
		case "gpt-6-astra", "gpt-6.1-sol":
			return 6, true
		}
	}
	return 0, false
}
