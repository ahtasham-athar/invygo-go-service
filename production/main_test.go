package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestMain configures offline, deterministic test runs against the snapshot CSV.
func TestMain(m *testing.M) {
	_ = os.Setenv("INVYGO_API_KEY", TEST_API_KEY)
	_ = os.Setenv("INVYGO_SHEET_FILE", "testdata/inventory.csv")
	os.Exit(m.Run())
}

func doRequest(t *testing.T, payload, apiKey string) (int, map[string]interface{}) {
	t.Helper()
	req, _ := http.NewRequest("POST", "/search", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}
	rr := httptest.NewRecorder()
	SearchHandler(rr, req)
	var resp map[string]interface{}
	if rr.Code == 200 {
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	}
	return rr.Code, resp
}

func results(t *testing.T, resp map[string]interface{}) []map[string]interface{} {
	t.Helper()
	raw, ok := resp["results"].([]interface{})
	if !ok {
		t.Fatalf("results missing or wrong type; status=%v", resp["status"])
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]interface{}))
	}
	return out
}

// ---------------- Pure-function unit tests (no data) ----------------

func TestCleanArea(t *testing.T) {
	cases := map[string]string{
		"Riyadh - As Sulimaniyah - Pickup":       "As Sulimaniyah",
		"Riyadh - Al Yarmuk":                     "Al Yarmuk",
		"Riyadh - Al Faisaliyyah - STO - Pickup": "Al Faisaliyyah",
		"Jeddah - An Naseem (Pick-up)":           "An Naseem",
		"Daily Sulimaniyah Riyadh":               "Sulimaniyah",
		"Daily Riyadh - Al Aqiq":                 "Al Aqiq",
		"Jeddah Sakr Qoraish":                    "Sakr Qoraish",
		"Al Khobar -  Al-Thuqbah":                "Al-Thuqbah",
		"Riyadh - An Nahdah (Pick - up )":        "An Nahdah",
		"JDH":                                    "",
		"ARAR":                                   "",
		"Olayah":                                 "Olayah",
		"":                                       "",
	}
	for in, want := range cases {
		if got := cleanArea(in, ""); got != want {
			t.Errorf("cleanArea(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeBodyType(t *testing.T) {
	cases := map[string]string{"Suv": "SUV", "SUV": "SUV", "Crossover": "Crossover", "cuv": "CUV", "": "", "Sedan": "Sedan"}
	for in, want := range cases {
		if got := normalizeBodyType(in); got != want {
			t.Errorf("normalizeBodyType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildBodyByModelMajority(t *testing.T) {
	cars := []Car{
		{Brand: "Hyundai", Model: "i10", BodyType: "Hatchback"},
		{Brand: "Hyundai", Model: "i10", BodyType: "Hatchback"},
		{Brand: "Hyundai", Model: "i10", BodyType: "Sedan"}, // minority -> ignored
		{Brand: "Toyota", Model: "Camry", BodyType: "Sedan"},
	}
	m := buildBodyByModel(cars)
	if m["hyundai i10"] != "Hatchback" {
		t.Errorf("majority vote failed for i10: got %q", m["hyundai i10"])
	}
	if m["toyota camry"] != "Sedan" {
		t.Errorf("expected Sedan for camry, got %q", m["toyota camry"])
	}
}

func TestFlexFloat(t *testing.T) {
	var s struct {
		P flexFloat `json:"p"`
	}
	for _, in := range []string{`{"p":2500}`, `{"p":"2500"}`, `{"p":"2,500"}`} {
		_ = json.Unmarshal([]byte(in), &s)
		if float64(s.P) != 2500 {
			t.Errorf("flexFloat(%s) = %v, want 2500", in, float64(s.P))
		}
	}
	for _, in := range []string{`{"p":null}`, `{"p":""}`, `{"p":"abc"}`} {
		s.P = 9
		_ = json.Unmarshal([]byte(in), &s)
		if float64(s.P) != 0 {
			t.Errorf("flexFloat(%s) = %v, want 0", in, float64(s.P))
		}
	}
}

// ---------------- HTTP / contract tests (snapshot data) ----------------

func TestAuth(t *testing.T) {
	if code, _ := doRequest(t, `{"city":"Riyadh"}`, ""); code != 401 {
		t.Errorf("missing key: got %d want 401", code)
	}
	if code, _ := doRequest(t, `{"city":"Riyadh"}`, "wrong"); code != 401 {
		t.Errorf("bad key: got %d want 401", code)
	}
}

func TestMissingCity(t *testing.T) {
	_, resp := doRequest(t, `{"query":"Toyota"}`, TEST_API_KEY)
	if resp["status"] != StatusError {
		t.Errorf("expected Error, got %v", resp["status"])
	}
}

func TestProductFilterIsolation(t *testing.T) {
	for _, p := range []string{"STO", "MONTHLY", "STS"} {
		_, resp := doRequest(t, `{"city":"Riyadh","product":"`+p+`"}`, TEST_API_KEY)
		for _, car := range results(t, resp) {
			if !strings.EqualFold(car["product"].(string), p) {
				t.Errorf("product=%s leaked %v", p, car["product"])
			}
		}
	}
}

func TestIdealBrandMatch(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","query":"Toyota"}`, TEST_API_KEY)
	if resp["status"] != StatusIdeal {
		t.Fatalf("expected %q, got %v", StatusIdeal, resp["status"])
	}
	rs := results(t, resp)
	if len(rs) == 0 || !strings.EqualFold(rs[0]["brand"].(string), "Toyota") {
		t.Errorf("expected Toyota first, got %v", rs)
	}
}

func TestBodyTypeFallbackAnchor(t *testing.T) {
	// Nonexistent model but explicit body_type -> Alternative on same body_type.
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","query":"Ferrari","body_type":"SUV","max_price":3000}`, TEST_API_KEY)
	st := resp["status"].(string)
	if st != StatusAlternative && st != StatusAboveBudget {
		t.Fatalf("expected alternative/above-budget, got %v", st)
	}
	for _, car := range results(t, resp) {
		if !strings.EqualFold(car["body_type"].(string), "SUV") {
			t.Errorf("fallback returned non-SUV: %v", car["body_type"])
		}
	}
}

func TestModelInferenceFallback(t *testing.T) {
	// Camry (a Sedan) not requested-as-available -> if absent, anchor Sedan via model map.
	_, resp := doRequest(t, `{"city":"Taif","product":"STO","query":"Camry","max_price":3000}`, TEST_API_KEY)
	st := resp["status"].(string)
	// Acceptable outcomes: ideal (it exists), alternative (sedan anchor), above-budget, budget-needed, no-body-match, no-cars.
	switch st {
	case StatusIdeal, StatusAlternative, StatusAboveBudget, StatusBudgetNeeded, StatusNoBodyMatch, StatusNoCars:
	default:
		t.Errorf("unexpected status for Camry/Taif: %v", st)
	}
}

func TestNoBudgetReturnsLadderedAlternative(t *testing.T) {
	// Body type known, STO/MONTHLY, NO budget -> never blocks: a price-laddered Alternative
	// with a numeric range (the retired "Budget Needed" dead-end is gone).
	_, resp := doRequest(t, `{"city":"Riyadh","product":"MONTHLY","query":"Zzz","body_type":"Sedan"}`, TEST_API_KEY)
	if resp["status"] != StatusAlternative {
		t.Fatalf("expected %q, got %v", StatusAlternative, resp["status"])
	}
	// Raw digits are stripped by design; the range ships as Arabic spoken words.
	assertNoRawDigits(t, resp, "no-budget ladder response")
	if s, _ := resp["price_min_spoken"].(string); s == "" {
		t.Error("price_min_spoken missing")
	}
	if s, _ := resp["price_max_spoken"].(string); s == "" {
		t.Error("price_max_spoken missing")
	}
	if len(results(t, resp)) == 0 {
		t.Error("expected a laddered spread, got no results")
	}
}

func TestNeedBodyTypeWhenUnanchorable(t *testing.T) {
	// Unknown model, no body_type, no tier -> ask for body type.
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","query":"Zxqwerty"}`, TEST_API_KEY)
	if resp["status"] != StatusNeedBodyType {
		t.Errorf("expected %q, got %v", StatusNeedBodyType, resp["status"])
	}
}

func TestSTSWeeklyBasisAndNoStarterFee(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STS"}`, TEST_API_KEY)
	rs := results(t, resp)
	if len(rs) == 0 {
		t.Skip("no STS in Riyadh snapshot")
	}
	c := rs[0]
	if c["price_basis"] != "weekly" {
		t.Errorf("STS price_basis = %v, want weekly", c["price_basis"])
	}
	// starter_fee is STO-only and raw digits never serialize: an STS card must have
	// neither the raw field nor a spoken form.
	if _, present := c["starter_fee"]; present {
		t.Errorf("raw starter_fee leaked on STS card: %v", c["starter_fee"])
	}
	if s, _ := c["starter_fee_spoken"].(string); s != "" {
		t.Errorf("STS starter_fee_spoken should be absent (10%% rule), got %q", s)
	}
	if note, _ := c["starter_fee_note"].(string); !strings.Contains(note, "10%") {
		t.Errorf("STS should carry 10%% starter_fee_note, got %q", note)
	}
}

func TestSTOExactStarterFee(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO"}`, TEST_API_KEY)
	rs := results(t, resp)
	if len(rs) == 0 {
		t.Skip("no STO in Riyadh snapshot")
	}
	if rs[0]["price_basis"] != "monthly" {
		t.Errorf("STO price_basis = %v, want monthly", rs[0]["price_basis"])
	}
	// STO carries no 10% note (exact fee used instead).
	if note, _ := rs[0]["starter_fee_note"].(string); note != "" {
		t.Errorf("STO should not carry a 10%% note, got %q", note)
	}
}

func TestCrossProductAndCityEnrichment(t *testing.T) {
	// A luxury model unlikely to be in Taif STS -> expect enrichment hints if it exists elsewhere.
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STS","query":"BMW"}`, TEST_API_KEY)
	// Not asserting specific cities; just that the field, when present, is a list.
	if v, ok := resp["also_in_products"]; ok {
		if _, isList := v.([]interface{}); !isList {
			t.Errorf("also_in_products should be a list, got %T", v)
		}
	}
}

func TestNoCarsForUnknownCity(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Atlantis"}`, TEST_API_KEY)
	if resp["status"] != StatusNoCars {
		t.Errorf("expected %q, got %v", StatusNoCars, resp["status"])
	}
}

// ---------------- New filtering features (A1–A3, B4, B5) ----------------

func TestFlexInt(t *testing.T) {
	var s struct {
		Y flexInt `json:"y"`
	}
	for _, in := range []string{`{"y":2024}`, `{"y":"2024"}`, `{"y":"2,024"}`, `{"y":"2024.0"}`} {
		_ = json.Unmarshal([]byte(in), &s)
		if int(s.Y) != 2024 {
			t.Errorf("flexInt(%s) = %v, want 2024", in, int(s.Y))
		}
	}
	for _, in := range []string{`{"y":null}`, `{"y":""}`, `{"y":"abc"}`} {
		s.Y = 9
		_ = json.Unmarshal([]byte(in), &s)
		if int(s.Y) != 0 {
			t.Errorf("flexInt(%s) = %v, want 0", in, int(s.Y))
		}
	}
}

// A3: No Cars Found must carry a concrete alternative (cities with stock).
func TestNoCarsCarriesAlternatives(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Atlantis","product":"STO"}`, TEST_API_KEY)
	if resp["status"] != StatusNoCars {
		t.Fatalf("expected %q, got %v", StatusNoCars, resp["status"])
	}
	cs, ok := resp["also_in_cities"].([]interface{})
	if !ok || len(cs) == 0 {
		t.Error("No Cars Found must carry also_in_cities alternatives")
	}
}

// A3: No Body Type Match must carry the body types we DO have.
func TestNoBodyMatchCarriesBodyTypes(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","body_type":"Van"}`, TEST_API_KEY)
	if resp["status"] != StatusNoBodyMatch {
		t.Skipf("Riyadh STO has Vans in snapshot (status=%v)", resp["status"])
	}
	bts, ok := resp["available_body_types"].([]interface{})
	if !ok || len(bts) == 0 {
		t.Error("No Body Type Match must carry available_body_types")
	}
}

// B4: min_price is a hard lower bound.
func TestMinPriceFloor(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","min_price":2500}`, TEST_API_KEY)
	for _, c := range results(t, resp) {
		if p, ok := spokenValue(t, c, "base_price_spoken"); ok && p < 2500 {
			t.Errorf("min_price=2500 leaked %v", p)
		}
	}
}

// B5: min_year drops older model years.
func TestMinYearFilter(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","min_year":2025}`, TEST_API_KEY)
	for _, c := range results(t, resp) {
		if y, ok := spokenValue(t, c, "year_spoken"); ok && int(y) < 2025 {
			t.Errorf("min_year=2025 leaked %v", y)
		}
	}
}

// A2: condition is soft — an unmatched condition broadens (with a note) instead of dead-ending.
func TestSoftConditionRelaxed(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","query":"Zzz","body_type":"Sedan","condition":"USED","max_price":4000}`, TEST_API_KEY)
	if st, _ := resp["status"].(string); st == StatusNoCars {
		t.Fatalf("soft condition should not yield No Cars, got %v", st)
	}
	if rf, ok := resp["relaxed_filters"].([]interface{}); ok {
		found := false
		for _, r := range rf {
			if r == "condition" {
				found = true
			}
		}
		if !found {
			t.Error("relaxed_filters should include condition when condition was broadened")
		}
	}
}

// B4: proportional band — a car within +10% of budget is an Alternative, not Above Budget.
func TestProportionalBand(t *testing.T) {
	// At budget 2500 the ceiling is 2750; assert any Alternative result respects it.
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","query":"Zzz","body_type":"Sedan","max_price":2500}`, TEST_API_KEY)
	if resp["status"] == StatusAlternative {
		for _, c := range results(t, resp) {
			if p, ok := spokenValue(t, c, "base_price_spoken"); ok && p > 2750 {
				t.Errorf("Alternative leaked %v above ceiling 2750", p)
			}
		}
	}
}
