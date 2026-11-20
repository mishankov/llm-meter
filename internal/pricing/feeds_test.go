package pricing

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestModelsDevUsesProviderScopedRatesAndRetainsKnownOpenAIRules(t *testing.T) {
	snapshot := fetchFixture(t, modelsDevSource, `{
		"openai":{"models":{
			"embedding":{"cost":{"input":0.13,"output":0}},
			"gpt-6-luna":{"cost":{"input":0.1,"output":0.5,"cache_read":0.01,"cache_write":0.125,"tiers":[
				{"input":0.2,"output":0.75,"cache_read":0.02,"cache_write":0.25,"tier":{"type":"context","size":272000}}
			]}},
			"gpt-5.6-sol":{"cost":{"input":4,"output":20}}
		}},
		"other-startup":{"models":{"embedding":{"cost":{"input":0,"output":0}}}}
	}`)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored Snapshot
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	var catalog Catalog
	catalog.SetSnapshot(restored)
	november := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	rates, _, source := catalog.ForProviderUsage("codex", "gpt-6-luna", "standard", november, 272001, 0, 0, 0, 1)
	if rates == nil || source != modelsDevSource || rates.Input != .2 || rates.Output != .75 {
		t.Fatalf("Models.dev context rates: %+v from %q", rates, source)
	}
	rates, _, source = catalog.ForProviderUsage("openai", "gpt-6-luna", "priority", november, 1000, 0, 0, 0, 1)
	if rates == nil || source != "builtin" || rates.Output != 1 {
		t.Fatalf("built-in priority rates: %+v from %q", rates, source)
	}
	rate, cost, source := catalog.ForProviderUsage("other-startup", "embedding", "", november, 100, 0, 0, 0, 0)
	if rate == nil || cost == nil || *cost != 0 || source != modelsDevSource {
		t.Fatalf("explicit free rate: %+v, %v from %q", rate, cost, source)
	}
	if _, cost, _ := catalog.ForProviderUsage("unmatched", "gpt-6-luna", "", november, 1000, 0, 0, 0, 1); cost != nil {
		t.Fatal("rate lookup fell across provider boundaries")
	}
	if _, cost, _ := catalog.ForProviderUsage("openai", "embedding", "", november, 1000, 1, 0, 0, 0); cost != nil {
		t.Fatal("missing cache-read rate was treated as free")
	}
	if _, cost, _ := catalog.ForProviderUsage("openai", "gpt-5.6-sol", "", november.AddDate(0, 1, 0), 1000, 0, 0, 0, 1); cost != nil {
		t.Fatal("expired promotional price was used from the feed")
	}
}

func TestOpenCodeGoRatesHonorTimeAndContext(t *testing.T) {
	var catalog Catalog
	catalog.SetSnapshot(fetchFixture(t, modelsDevSource, `{"opencode-go":{"models":{
		"deepseek-v4.1-flash":{"cost":{"input":9,"output":9}}, "unsupported":{"cost":{"input":1,"output":1}}
	}}}`))
	catalog.SetSnapshot(fetchFixture(t, opencodeGoSource, `{"data":[
		{"id":"deepseek-v4.1-flash","cost":{
			"text":{"input":{"price":0.15,"cache":{"read":0.003}},"output":{"price":0.6}},
			"multipliers":[{"when":{"utc_days":{"in":["mon","tue","wed","thu","fri"]},"any":[
				{"all":[{"utc_hours":{"gte":"01:00"}},{"utc_hours":{"lt":"04:00"}}]},
				{"all":[{"utc_hours":{"gte":"06:00"}},{"utc_hours":{"lt":"10:00"}}]}
			]},"factor":2}]
		}},
		{"id":"gpt-6-luna","cost":{"text":{
			"input":{"price":0.1,"cache":{"read":0.01,"write":{"30m":0.125,"12h":0.25}},"overrides":[
				{"when":{"context":{"gt":272000}},"price":0.2,"cache":{"read":0.02,"write":{"30m":0.25,"12h":0.5}}}
			]},"output":{"price":0.5,"overrides":[{"when":{"context":{"gt":272000}},"price":0.75}]}
		}}},
		{"id":"unsupported","cost":{"text":{"input":{"price":1},"output":{"price":1}},
			"multipliers":[{"when":{"region":{"in":["eu"]}},"factor":2}]}}
	]}`))
	friday := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		hour time.Duration
		rate float64
	}{
		{1, .30}, {4, .15}, {8, .30}, {10, .15}, {26, .15},
	} {
		at := friday.Add(test.hour * time.Hour).In(time.FixedZone("local", 4*60*60))
		rates, _, source := catalog.ForProviderUsage(opencodeGoSource, "deepseek-v4.1-flash", "", at, 2000, 1000, 0, 0, 500)
		if rates == nil || rates.Input != test.rate || source != opencodeGoSource {
			t.Fatalf("Go rates at %v: %+v from %q", at, rates, source)
		}
	}
	rates, _, source := catalog.ForProviderUsage(opencodeGoSource, "gpt-6-luna", "", friday, 272001, 20, 10, 5, 10)
	if rates == nil || rates.CacheWrite != .25 || rates.CacheWrite12h != .5 || rates.Output != .75 || source != opencodeGoSource {
		t.Fatalf("Go long-context cache rates: %+v from %q", rates, source)
	}
	if _, cost, _ := catalog.ForProviderUsage(opencodeGoSource, "unsupported", "", friday, 1000, 0, 0, 0, 1); cost != nil {
		t.Fatal("unsupported rate rules were silently discarded")
	}
}

func TestFetchRejectsEmptyRateFeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	if _, err := Fetch(t.Context(), server.Client(), Feed{Name: opencodeGoSource, URL: server.URL}); err == nil {
		t.Fatal("empty feed replaced valid snapshot")
	}
}

func fetchFixture(t *testing.T, name, body string) Snapshot {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "" {
			t.Error("pricing feed request carried credentials")
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	snapshot, err := Fetch(t.Context(), server.Client(), Feed{Name: name, URL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
