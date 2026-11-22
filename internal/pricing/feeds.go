package pricing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
	modelsDevSource  = "models.dev"
	opencodeGoSource = "opencode-go"
	maxFeedBytes     = 16 << 20
)

type Feed struct {
	Name string
	URL  string
}

var DefaultFeeds = []Feed{
	{Name: modelsDevSource, URL: "https://models.dev/api.json?type=all"},
	{Name: opencodeGoSource, URL: "https://opencode.ai/inference/go/v1/models"},
}

// Snapshot stores validated text rates in dollars per million tokens.
type Snapshot struct {
	Source            string                           `json:"source"`
	URL               string                           `json:"url"`
	FetchedAt         time.Time                        `json:"fetchedAt"`
	Providers         map[string]map[string]modelPrice `json:"providers"`
	AudioRatesChecked bool                             `json:"audioRatesChecked,omitempty"`
}

// The pointer fields distinguish absent rates from explicitly free usage.
type partialRates struct {
	Input         *float64 `json:"input,omitempty"`
	Output        *float64 `json:"output,omitempty"`
	CachedInput   *float64 `json:"cache_read,omitempty"`
	CacheWrite    *float64 `json:"cache_write,omitempty"`
	CacheWrite12h *float64 `json:"cache_write_12h,omitempty"`
}

func (r partialRates) valid() bool {
	for _, rate := range []*float64{r.Input, r.Output, r.CachedInput, r.CacheWrite, r.CacheWrite12h} {
		if rate != nil && *rate < 0 {
			return false
		}
	}
	return true
}

func (r *partialRates) overlay(override partialRates) {
	pairs := []struct {
		dst **float64
		src *float64
	}{
		{&r.Input, override.Input}, {&r.Output, override.Output}, {&r.CachedInput, override.CachedInput},
		{&r.CacheWrite, override.CacheWrite}, {&r.CacheWrite12h, override.CacheWrite12h},
	}
	for _, pair := range pairs {
		if pair.src != nil {
			*pair.dst = pair.src
		}
	}
}

type modelPrice struct {
	Rates              partialRates     `json:"rates"`
	Overrides          []rateOverride   `json:"overrides,omitempty"`
	Multipliers        []rateMultiplier `json:"multipliers,omitempty"`
	Unsupported        bool             `json:"unsupported,omitempty"`
	SeparateAudioRates bool             `json:"separateAudioRates,omitempty"`
}

type rateOverride struct {
	When  condition    `json:"when"`
	Rates partialRates `json:"rates"`
}

type rateMultiplier struct {
	When   condition `json:"when"`
	Factor float64   `json:"factor"`
}

type bounds[T int64 | string] struct {
	GT  *T `json:"gt,omitempty"`
	GTE *T `json:"gte,omitempty"`
	LT  *T `json:"lt,omitempty"`
	LTE *T `json:"lte,omitempty"`
}

func (b *bounds[T]) matches(value T) bool {
	return b == nil || (b.GT == nil || value > *b.GT) && (b.GTE == nil || value >= *b.GTE) &&
		(b.LT == nil || value < *b.LT) && (b.LTE == nil || value <= *b.LTE)
}

type weekdays struct {
	In []string `json:"in"`
}

type condition struct {
	Context  *bounds[int64]  `json:"context,omitempty"`
	Date     *bounds[string] `json:"date,omitempty"`
	UTCHours *bounds[string] `json:"utc_hours,omitempty"`
	UTCDays  *weekdays       `json:"utc_days,omitempty"`
	All      []condition     `json:"all,omitempty"`
	Any      []condition     `json:"any,omitempty"`
}

func (c condition) matches(at time.Time, input int64) bool {
	if !c.Context.matches(input) || !c.Date.matches(at.Format(time.DateOnly)) || !c.UTCHours.matches(at.Format("15:04")) {
		return false
	}
	if c.UTCDays != nil && !slices.Contains(c.UTCDays.In, strings.ToLower(at.Weekday().String()[:3])) {
		return false
	}
	for _, item := range c.All {
		if !item.matches(at, input) {
			return false
		}
	}
	return c.Any == nil || slices.ContainsFunc(c.Any, func(item condition) bool { return item.matches(at, input) })
}

func parseCondition(data json.RawMessage) (condition, bool) {
	var c condition
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return c, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, false
	}
	return c, c.valid(0)
}

func (c condition) valid(depth int) bool {
	if depth >= 12 || !validTimeBounds(c.Date, time.DateOnly) || !validTimeBounds(c.UTCHours, "15:04") {
		return false
	}
	if c.All != nil && len(c.All) == 0 || c.Any != nil && len(c.Any) == 0 {
		return false
	}
	if c.UTCDays != nil {
		if len(c.UTCDays.In) == 0 {
			return false
		}
		for _, day := range c.UTCDays.In {
			if !slices.Contains([]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, day) {
				return false
			}
		}
	}
	return !slices.ContainsFunc(c.All, func(child condition) bool { return !child.valid(depth + 1) }) &&
		!slices.ContainsFunc(c.Any, func(child condition) bool { return !child.valid(depth + 1) })
}

func validTimeBounds(b *bounds[string], layout string) bool {
	if b == nil {
		return true
	}
	for _, value := range []*string{b.GT, b.GTE, b.LT, b.LTE} {
		if value != nil {
			parsed, err := time.Parse(layout, *value)
			if err != nil || parsed.Format(layout) != *value {
				return false
			}
		}
	}
	return true
}

func Fetch(ctx context.Context, client *http.Client, feed Feed) (Snapshot, error) {
	var result Snapshot
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.URL, nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "llm-meter/1.0")
	response, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("pricing feed returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return result, err
	}
	if len(data) > maxFeedBytes {
		return result, fmt.Errorf("pricing feed exceeds %d bytes", maxFeedBytes)
	}
	var providers map[string]map[string]modelPrice
	switch feed.Name {
	case modelsDevSource:
		providers, err = parseModelsDev(data)
	case opencodeGoSource:
		providers, err = parseOpenCodeGo(data)
	default:
		err = fmt.Errorf("unknown pricing feed %q", feed.Name)
	}
	if err != nil {
		return result, err
	}
	for _, models := range providers {
		for _, model := range models {
			if !model.Unsupported && !model.SeparateAudioRates && (model.Rates.Input != nil || model.Rates.Output != nil) {
				return Snapshot{
					Source: feed.Name, URL: feed.URL, FetchedAt: time.Now().UTC(), Providers: providers,
					AudioRatesChecked: feed.Name == modelsDevSource,
				}, nil
			}
		}
	}
	return result, fmt.Errorf("pricing feed contains no valid text rates")
}

type modelsDevRates struct {
	partialRates
	AudioInput  *float64 `json:"input_audio"`
	AudioOutput *float64 `json:"output_audio"`
}

func differingAudioRate(text, audio *float64, tiered bool) bool {
	// With context tiers, the base audio rate can differ from an effective text rate.
	return audio != nil && (tiered || text == nil || *audio != *text)
}

type modelsDevPrice struct {
	modelsDevRates
	Tiers []struct {
		modelsDevRates
		Tier struct {
			Type string `json:"type"`
			Size *int64 `json:"size"`
		} `json:"tier"`
	} `json:"tiers"`
}

func parseModelsDev(data []byte) (map[string]map[string]modelPrice, error) {
	var catalog map[string]struct {
		Models map[string]struct {
			Cost *modelsDevPrice `json:"cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, err
	}
	result := make(map[string]map[string]modelPrice, len(catalog))
	for provider, entry := range catalog {
		models := make(map[string]modelPrice)
		for id, model := range entry.Models {
			if model.Cost == nil || !model.Cost.partialRates.valid() {
				continue
			}
			tiered := len(model.Cost.Tiers) != 0
			price := modelPrice{
				Rates: model.Cost.partialRates,
				SeparateAudioRates: differingAudioRate(model.Cost.Input, model.Cost.AudioInput, tiered) ||
					differingAudioRate(model.Cost.Output, model.Cost.AudioOutput, tiered),
			}
			for _, tier := range model.Cost.Tiers {
				price.SeparateAudioRates = price.SeparateAudioRates || tier.AudioInput != nil || tier.AudioOutput != nil
				if tier.Tier.Type != "context" || tier.Tier.Size == nil || *tier.Tier.Size < 0 || !tier.partialRates.valid() {
					price.Unsupported = true
					break
				}
				price.Overrides = append(price.Overrides, rateOverride{
					When: condition{Context: &bounds[int64]{GT: tier.Tier.Size}}, Rates: tier.partialRates,
				})
			}
			slices.SortStableFunc(price.Overrides, func(a, b rateOverride) int {
				if *a.When.Context.GT < *b.When.Context.GT {
					return -1
				}
				if *a.When.Context.GT > *b.When.Context.GT {
					return 1
				}
				return 0
			})
			models[id] = price
		}
		if len(models) != 0 {
			result[provider] = models
		}
	}
	return result, nil
}

type openCodePrice struct {
	Price *float64 `json:"price"`
	Cache *struct {
		Read  *float64        `json:"read"`
		Write json.RawMessage `json:"write"`
	} `json:"cache"`
	Overrides []struct {
		When json.RawMessage `json:"when"`
		openCodePrice
	} `json:"overrides"`
}

func (p openCodePrice) rates(input bool) (r partialRates, ok bool) {
	if !input {
		r.Output = p.Price
		return r, r.valid()
	}
	r.Input = p.Price
	if p.Cache == nil {
		return r, r.valid()
	}
	r.CachedInput = p.Cache.Read
	if len(p.Cache.Write) > 0 && !bytes.Equal(p.Cache.Write, []byte("null")) {
		if err := json.Unmarshal(p.Cache.Write, &r.CacheWrite); err != nil {
			var prices map[string]float64
			if err := json.Unmarshal(p.Cache.Write, &prices); err != nil {
				return r, false
			}
			if price, present := prices["30m"]; present {
				r.CacheWrite = &price
			}
			if price, present := prices["12h"]; present {
				r.CacheWrite12h = &price
			}
		}
	}
	return r, r.valid()
}

func parseOpenCodeGo(data []byte) (map[string]map[string]modelPrice, error) {
	var catalog struct {
		Data []struct {
			ID   string `json:"id"`
			Cost *struct {
				Text struct {
					Input  openCodePrice `json:"input"`
					Output openCodePrice `json:"output"`
				} `json:"text"`
				Multipliers []struct {
					When   json.RawMessage `json:"when"`
					Factor json.RawMessage `json:"factor"`
				} `json:"multipliers"`
			} `json:"cost"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, err
	}
	result := make(map[string]modelPrice, len(catalog.Data))
	for _, model := range catalog.Data {
		if model.ID == "" {
			continue
		}
		price := modelPrice{Unsupported: true}
		if model.Cost != nil {
			price.Unsupported = false
			for _, field := range []struct {
				input bool
				item  openCodePrice
			}{
				{true, model.Cost.Text.Input}, {false, model.Cost.Text.Output},
			} {
				base, ok := field.item.rates(field.input)
				price.Rates.overlay(base)
				price.Unsupported = price.Unsupported || !ok
				for _, override := range field.item.Overrides {
					when, valid := parseCondition(override.When)
					rates, ok := override.openCodePrice.rates(field.input)
					price.Unsupported = price.Unsupported || !valid || !ok
					price.Overrides = append(price.Overrides, rateOverride{When: when, Rates: rates})
				}
			}
			for _, multiplier := range model.Cost.Multipliers {
				when, valid := parseCondition(multiplier.When)
				var factor *float64
				err := json.Unmarshal(multiplier.Factor, &factor)
				if !valid || err != nil || factor == nil || *factor < 0 {
					price.Unsupported = true
					break
				}
				price.Multipliers = append(price.Multipliers, rateMultiplier{When: when, Factor: *factor})
			}
		}
		result[model.ID] = price
	}
	return map[string]map[string]modelPrice{opencodeGoSource: result}, nil
}
