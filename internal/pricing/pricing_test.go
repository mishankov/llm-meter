package pricing

import (
	"testing"
	"time"
)

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
