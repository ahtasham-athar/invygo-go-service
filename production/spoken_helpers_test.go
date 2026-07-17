package main

import (
	"sync"
	"testing"
)

// The API contract intentionally strips every raw digit the agent could misread
// (base_price, starter_fee, year, price_min, price_max, cheapest) and emits only
// the deterministic Arabic word forms (*_spoken). Tests therefore verify numeric
// invariants by reverse-mapping those spoken words back to numbers. The map is
// built from the SAME renderer (arMoneyWords / arNumberWords) over the values
// that can actually appear in a response: every inventory price, starter fee,
// and model year. Rendering is deterministic, so the lookup is exact.
var (
	spokenOnce sync.Once
	spokenNum  map[string]float64
)

func ensureSpokenMap() {
	spokenOnce.Do(func() {
		spokenNum = map[string]float64{}
		cars, _, err := getInventory()
		if err != nil {
			return
		}
		for _, c := range cars {
			if c.BasePrice > 0 {
				spokenNum[arMoneyWords(c.BasePrice)] = c.BasePrice
			}
			if c.StarterFee > 0 {
				spokenNum[arMoneyWords(c.StarterFee)] = c.StarterFee
			}
			if c.Year > 0 {
				spokenNum[arNumberWords(c.Year)] = float64(c.Year)
			}
		}
	})
}

func buildSpokenMap(t *testing.T) {
	t.Helper()
	ensureSpokenMap()
	if len(spokenNum) == 0 {
		t.Fatal("spoken reverse map is empty — inventory failed to load")
	}
}

// spokenValue resolves a *_spoken field on a result card back to its number.
// Returns (0, false) when the field is absent or empty.
func spokenValue(t *testing.T, card map[string]interface{}, key string) (float64, bool) {
	t.Helper()
	buildSpokenMap(t)
	s, _ := card[key].(string)
	if s == "" {
		return 0, false
	}
	v, ok := spokenNum[s]
	if !ok {
		t.Errorf("%s %q does not reverse-map to any inventory value (renderer drift?)", key, s)
		return 0, false
	}
	return v, true
}

// assertNoRawDigits fails if any of the intentionally-stripped numeric fields
// leaked back into a response object (top level or a result card).
func assertNoRawDigits(t *testing.T, obj map[string]interface{}, where string) {
	t.Helper()
	for _, k := range []string{"base_price", "starter_fee", "year", "price_min", "price_max", "cheapest"} {
		if _, present := obj[k]; present {
			t.Errorf("raw digit field %q leaked in %s — the agent must only ever see *_spoken forms", k, where)
		}
	}
}
