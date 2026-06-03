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
		"Riyadh - As Sulimaniyah - Pickup":        "As Sulimaniyah",
		"Riyadh - Al Yarmuk":                       "Al Yarmuk",
		"Riyadh - Al Faisaliyyah - STO - Pickup":   "Al Faisaliyyah",
		"Jeddah - An Naseem (Pick-up)":             "An Naseem",
		"Daily Sulimaniyah Riyadh":                 "Sulimaniyah",
		"Daily Riyadh - Al Aqiq":                   "Al Aqiq",
		"Jeddah Sakr Qoraish":                      "Sakr Qoraish",
		"Al Khobar -  Al-Thuqbah":                  "Al-Thuqbah",
		"Riyadh - An Nahdah (Pick - up )":          "An Nahdah",
		"JDH":                                      "",
		"ARAR":                                     "",
		"Olayah":                                   "Olayah",
		"":                                         "",
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

func TestBudgetNeededWhenNoBudget(t *testing.T) {
	// Body type known, STO/MONTHLY, no budget -> ask, with a numeric range.
	_, resp := doRequest(t, `{"city":"Riyadh","product":"MONTHLY","query":"Zzz","body_type":"Sedan"}`, TEST_API_KEY)
	if resp["status"] != StatusBudgetNeeded {
		t.Fatalf("expected %q, got %v", StatusBudgetNeeded, resp["status"])
	}
	if _, ok := resp["price_min"]; !ok {
		t.Error("price_min missing for Budget Needed")
	}
	if _, ok := resp["price_max"]; !ok {
		t.Error("price_max missing for Budget Needed")
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
	if c["starter_fee"].(float64) != 0 {
		t.Errorf("STS starter_fee should be 0 (10%% rule), got %v", c["starter_fee"])
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
