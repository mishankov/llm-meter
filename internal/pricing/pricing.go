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
	snapshots map[string]Snapshot
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

func (c *Catalog) SetSnapshot(snapshot Snapshot) {
	if c.snapshots == nil {
		c.snapshots = make(map[string]Snapshot)
	}
	c.snapshots[snapshot.Source] = snapshot
}

func (c Catalog) ForUsage(model, tier string, at time.Time, input, cached, writes, writes12h, output int64) (*Rates, *float64) {
	rates, cost, _ := c.ForProviderUsage("openai", model, tier, at, input, cached, writes, writes12h, output)
	return rates, cost
}

func (c Catalog) ForProviderUsage(provider, model, tier string, at time.Time, input, cached, writes, writes12h, output int64) (*Rates, *float64, string) {
	if input < 0 || cached < 0 || writes < 0 || writes12h < 0 || writes12h > writes || output < 0 || cached > input || writes > input-cached {
		return nil, nil, ""
	}
	counts := tokenCounts{input - cached - writes, cached, writes - writes12h, writes12h, output}
	if rates, ok := c.overrides[provider+"/"+model]; ok {
		return estimate(rates, counts, "override")
	}
	if rates, ok := c.overrides[model]; ok {
		return estimate(rates, counts, "override")
	}

	if provider == "codex" {
		provider = "openai"
	}
	base := model
	if provider == "openai" {
		base = openAIModel(model)
	}
	switch tier {
	case "", "auto", "default", "standard":
		if provider == opencodeGoSource {
			if price, ok := c.snapshots[opencodeGoSource].Providers[provider][model]; ok {
				rates, usable := price.resolve(at.UTC(), input, counts)
				if !usable {
					return nil, nil, ""
				}
				return estimate(rates, counts, opencodeGoSource)
			}
		}
		price, found := c.snapshots[modelsDevSource].Providers[provider][model]
		if !found && base != model {
			price, found = c.snapshots[modelsDevSource].Providers[provider][base]
		}
		if found && usableDownloadedRate(provider, base, at, input, price) {
			if rates, ok := price.resolve(at.UTC(), input, counts); ok {
				return estimate(rates, counts, modelsDevSource)
			}
		}
	}
	if provider == "openai" {
		if rates, ok := builtinRates(base, tier, at, input, writes); ok {
			return estimate(rates, counts, "builtin")
		}
	}
	return nil, nil, ""
}

type tokenCounts struct {
	input, cached, writes, writes12h, output int64
}

func estimate(r Rates, counts tokenCounts, source string) (*Rates, *float64, string) {
	if !r.valid() {
		return nil, nil, ""
	}
	cost := (float64(counts.input)*r.Input + float64(counts.cached)*r.CachedInput +
		float64(counts.writes)*r.CacheWrite + float64(counts.writes12h)*r.CacheWrite12h + float64(counts.output)*r.Output) / 1_000_000
	if math.IsInf(cost, 0) || math.IsNaN(cost) {
		return nil, nil, ""
	}
	return &r, &cost, source
}

func (m modelPrice) resolve(at time.Time, input int64, counts tokenCounts) (Rates, bool) {
	if m.Unsupported {
		return Rates{}, false
	}
	rates := m.Rates
	for _, override := range m.Overrides {
		if override.When.matches(at, input) {
			rates.overlay(override.Rates)
		}
	}
	if rates.Input == nil && rates.Output == nil {
		return Rates{}, false
	}
	factor := 1.0
	for _, multiplier := range m.Multipliers {
		if multiplier.When.matches(at, input) {
			factor *= multiplier.Factor
		}
	}
	var resolved Rates
	fields := []struct {
		rate  *float64
		count int64
		dst   *float64
	}{
		{rates.Input, counts.input, &resolved.Input},
		{rates.CachedInput, counts.cached, &resolved.CachedInput},
		{rates.CacheWrite, counts.writes, &resolved.CacheWrite},
		{rates.CacheWrite12h, counts.writes12h, &resolved.CacheWrite12h},
		{rates.Output, counts.output, &resolved.Output},
	}
	for _, field := range fields {
		if field.rate != nil {
			*field.dst = *field.rate * factor
		} else if field.count > 0 {
			return Rates{}, false
		}
	}
	return resolved, resolved.valid()
}

func openAIModel(model string) string {
	base := snapshotSuffix.ReplaceAllString(model, "")
	if base == "gpt-5.6" {
		return "gpt-5.6-sol"
	}
	return base
}

func usableDownloadedRate(provider, model string, at time.Time, input int64, price modelPrice) bool {
	if provider != "openai" {
		return true
	}
	// A flat catalog entry can still contain the expired GPT-5.6 promotional
	// rates. A replacement price can be used once the feed publishes it.
	if promotionEnded(model, at) && price.Rates.Input != nil && price.Rates.Output != nil &&
		*price.Rates.Input == standard[model].Input && *price.Rates.Output == standard[model].Output {
		return false
	}
	if input > 272_000 && longContextModel(model) {
		for _, override := range price.Overrides {
			if override.When.Context != nil && override.When.matches(at.UTC(), input) &&
				override.Rates.Input != nil && override.Rates.Output != nil {
				return true
			}
		}
		return false
	}
	return true
}

func promotionEnded(model string, at time.Time) bool {
	return model == "gpt-5.6-sol" && !at.Before(time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC))
}

func longContextModel(model string) bool {
	switch model {
	case "gpt-6.2-astra", "gpt-6.2-sol", "gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.5", "gpt-5.4":
		return true
	}
	return false
}

func builtinRates(base, tier string, at time.Time, input, writes int64) (Rates, bool) {
	rates, ok := standard[base]
	if !ok || (writes > 0 && rates.CacheWrite == 0) || promotionEnded(base, at) {
		return Rates{}, false
	}
	if longContextModel(base) && input > 272_000 {
		rates.Input *= 2
		rates.CachedInput *= 2
		rates.CacheWrite *= 2
		rates.Output *= 1.5
	}
	multiplier, ok := tierMultiplier(base, tier, at)
	if !ok {
		return Rates{}, false
	}
	rates.Input *= multiplier
	rates.CachedInput *= multiplier
	rates.Output *= multiplier
	rates.CacheWrite *= multiplier
	rates.CacheWrite12h = rates.CacheWrite * 2
	return rates, true
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
