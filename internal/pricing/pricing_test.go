package pricing

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOverridesDistinguishMissingRatesFromExplicitZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(path, []byte(`{
		"custom":{"input":2,"output":10,"cacheWrite":2.5},
		"provider/custom":{"input":2,"output":10,"cachedInput":0,"cacheWrite12h":0}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, provider          string
		cached, writes, longTTL int64
		priced                  bool
		cost                    float64
	}{
		{name: "uncached", priced: true, cost: .002},
		{name: "missing cache read", cached: 1000},
		{name: "missing long cache write", writes: 1000, longTTL: 1000},
		{name: "explicit free cache read", provider: "provider", cached: 1000, priced: true},
		{name: "explicit free long cache write", provider: "provider", writes: 1000, longTTL: 1000, priced: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rates, cost, source := catalog.ForProviderUsage(tt.provider, "custom", "", time.Time{}, 1000, tt.cached, tt.writes, tt.longTTL, 0)
			if !tt.priced {
				if rates != nil || cost != nil || source != "" {
					t.Fatalf("expected unpriced usage, got rates %v, cost %v, source %q", rates, cost, source)
				}
				return
			}
			if rates == nil || cost == nil || *cost != tt.cost || source != "override" {
				t.Fatalf("expected override cost %f, got rates %v, cost %v, source %q", tt.cost, rates, cost, source)
			}
		})
	}
}

func TestFastRatesForGPT55And56(t *testing.T) {
	catalog := Catalog{}
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		model         string
		input, output float64
	}{
		{model: "gpt-5.5", input: 12.5, output: 75},
		{model: "gpt-5.6-sol", input: 8, output: 40},
	} {
		rates, _ := catalog.ForUsage(tc.model, "priority", at, 100, 0, 0, 0, 10)
		if rates == nil || rates.Input != tc.input || rates.Output != tc.output {
			t.Fatalf("%s fast rates: %+v", tc.model, rates)
		}
	}
}
