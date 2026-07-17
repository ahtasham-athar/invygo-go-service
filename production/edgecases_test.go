package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// invariantProblems checks every cross-cutting guarantee that must hold for ANY
// response, given the request fields. Returns a list of violations (empty = OK).
func invariantProblems(reqProduct, city, cond, body string, budget float64, resp map[string]interface{}) []string {
	ensureSpokenMap()
	var p []string
	add := func(f string, a ...interface{}) { p = append(p, fmt.Sprintf(f, a...)) }

	status, _ := resp["status"].(string)
	if status == "" {
		add("empty status")
	}
	rs, ok := resp["results"].([]interface{})
	if !ok {
		add("results not an array (status=%s)", status)
		return p
	}
	if len(rs) > 4 {
		add("results>4 (%d, status=%s)", len(rs), status)
	}
	for _, ri := range rs {
		c, _ := ri.(map[string]interface{})
		cl := func(k string) string { s, _ := c[k].(string); return s }
		// Raw digits are stripped by design; numeric invariants read the *_spoken
		// forms and reverse-map them (see spoken_helpers_test.go).
		for _, k := range []string{"base_price", "starter_fee", "year"} {
			if _, present := c[k]; present {
				add("raw digit field %q leaked on %q", k, cl("description"))
			}
		}
		price, priced := spokenNum[cl("base_price_spoken")]

		if !strings.EqualFold(cl("city"), strings.TrimSpace(city)) {
			add("city leak %q!=%q", cl("city"), city)
		}
		if reqProduct != "" && !strings.EqualFold(cl("product"), reqProduct) {
			add("product leak %q want %s", cl("product"), reqProduct)
		}
		if cl("base_price_spoken") == "" || !priced || price <= 0 {
			add("unpriced result %q (base_price_spoken=%q)", cl("description"), cl("base_price_spoken"))
		}
		prod, basis := strings.ToUpper(cl("product")), cl("price_basis")
		if prod == "STS" && basis != "weekly" {
			add("STS basis=%q", basis)
		}
		if prod != "STS" && basis != "monthly" {
			add("%s basis=%q", prod, basis)
		}
		note := cl("starter_fee_note")
		if prod == "STO" {
			if note != "" {
				add("STO carries 10%% note")
			}
		} else {
			if !strings.Contains(note, "10%") {
				add("%s missing 10%% note", prod)
			}
			if cl("starter_fee_spoken") != "" {
				add("%s carries starter_fee_spoken %q (must be STO-only)", prod, cl("starter_fee_spoken"))
			}
		}
		// Condition is honored on Ideal; in fallback it may be broadened (reported via relaxed_filters).
		if cond != "" && status == StatusIdeal && !strings.EqualFold(cl("condition"), cond) {
			add("condition leak %q!=%q", cl("condition"), cond)
		}
		if body != "" && !strings.EqualFold(cl("body_type"), body) {
			add("body leak %q!=%q (status=%s)", cl("body_type"), body, status)
		}
		// Budget cap applies to ALL plans on positive-match statuses (STS included —
		// a weekly budget is honored the same way since the STS max_price fix).
		if budget > 0 && priced && (status == StatusIdeal || status == StatusAlternative) && price > bandCeiling(budget) {
			add("budget cap %.0f>ceiling %.0f (status=%s)", price, bandCeiling(budget), status)
		}
	}
	return p
}

// TestExhaustiveInvariantSweep fires the full cartesian product of
// city × product × body_type × budget × condition and asserts every invariant
// on every response. (~4,860 distinct payloads.)
func TestExhaustiveInvariantSweep(t *testing.T) {
	cities := []string{"Riyadh", "Jeddah", "Dammam", "Al Khobar", "Madinah", "Mecca", "Taif", "Arar", "Turayf"}
	products := []string{"", "STO", "MONTHLY", "STS"}
	bodies := []string{"", "Sedan", "SUV", "Crossover", "CUV", "Hatchback", "Minivan", "Pickup", "Van"}
	budgets := []float64{0, 300, 1500, 2500, 5000}
	conds := []string{"", "NEW", "USED"}

	var violations []string
	statusSeen := map[string]int{}
	n := 0

	for _, city := range cities {
		for _, prod := range products {
			for _, body := range bodies {
				for _, bud := range budgets {
					for _, cond := range conds {
						pl := map[string]interface{}{"city": city}
						if prod != "" {
							pl["product"] = prod
						}
						if body != "" {
							pl["body_type"] = body
						}
						if bud > 0 {
							pl["max_price"] = bud
						}
						if cond != "" {
							pl["condition"] = cond
						}
						b, _ := json.Marshal(pl)
						code, resp := doRequest(t, string(b), TEST_API_KEY)
						n++
						if code != 200 {
							t.Fatalf("HTTP %d for %s", code, b)
						}
						statusSeen[resp["status"].(string)]++
						for _, v := range invariantProblems(prod, city, cond, body, bud, resp) {
							violations = append(violations, fmt.Sprintf("%s :: %s", b, v))
							if len(violations) > 25 {
								t.Fatalf("too many invariant violations; first 25:\n%s", strings.Join(violations, "\n"))
							}
						}
					}
				}
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("invariant violations:\n%s", strings.Join(violations, "\n"))
	}
	t.Logf("swept %d payload combinations, all invariants held", n)
	t.Logf("status distribution: %v", statusSeen)
}

// TestAllStatusesReachable proves each documented status is actually produced.
func TestAllStatusesReachable(t *testing.T) {
	cases := []struct {
		want, payload string
	}{
		{StatusIdeal, `{"city":"Riyadh","product":"STO","query":"Toyota"}`},
		{StatusAlternative, `{"city":"Riyadh","product":"STO","query":"Ferrari","body_type":"Sedan","max_price":2500}`},
		{StatusAboveBudget, `{"city":"Riyadh","product":"STO","body_type":"SUV","max_price":300}`},
		{StatusAlternative, `{"city":"Riyadh","product":"MONTHLY","query":"Zzz","body_type":"Sedan"}`}, // no budget -> laddered Alternative (Budget Needed retired)
		{StatusNoBodyMatch, `{"city":"Riyadh","product":"STO","body_type":"Van"}`},
		{StatusNeedBodyType, `{"city":"Riyadh","product":"STO","query":"Zxqwerty"}`},
		{StatusNoCars, `{"city":"Atlantis"}`},
		{StatusUpsell, `{"city":"Riyadh","product":"STO","color":"Magenta"}`},
		{StatusError, `{"query":"Toyota"}`},
	}
	for _, c := range cases {
		_, resp := doRequest(t, c.payload, TEST_API_KEY)
		got, _ := resp["status"].(string)
		if got != c.want {
			t.Errorf("payload %s -> status %q, want %q", c.payload, got, c.want)
		} else {
			t.Logf("OK  %-20s <- %s", got, c.payload)
		}
	}
}

// TestRobustnessAndMalformedInputs exercises bad/odd inputs.
func TestRobustnessAndMalformedInputs(t *testing.T) {
	// Malformed JSON -> 400
	if code, _ := doRequest(t, `{`, TEST_API_KEY); code != 400 {
		t.Errorf("malformed JSON: got %d want 400", code)
	}
	// max_price as string / comma-string / null / garbage / negative -> 200, no crash
	for _, pl := range []string{
		`{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":"2,500"}`,
		`{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":"2500"}`,
		`{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":null}`,
		`{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":"abc"}`,
		`{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":-100}`,
		`{"city":" riYAdh ","product":"sto","query":"tOyOtA","condition":"used"}`, // case/space tolerance
		`{"city":"Riyadh","tier":null,"color":null,"query":null,"body_type":null,"condition":null,"max_price":null}`,
		`{"city":"Riyadh","query":"","tier":"","body_type":"","color":"","condition":""}`,
	} {
		code, resp := doRequest(t, pl, TEST_API_KEY)
		if code != 200 {
			t.Errorf("payload %s -> HTTP %d", pl, code)
			continue
		}
		if s, _ := resp["status"].(string); s == "" {
			t.Errorf("payload %s -> empty status", pl)
		}
	}
}

// TestColorIsolationIdeal: an explicit color, when satisfied, yields only that color.
func TestColorIsolationIdeal(t *testing.T) {
	_, resp := doRequest(t, `{"city":"Riyadh","product":"STO","color":"White"}`, TEST_API_KEY)
	if resp["status"] != StatusIdeal {
		t.Skipf("no white STO in Riyadh snapshot (status=%v)", resp["status"])
	}
	for _, ri := range results(t, resp) {
		colors, _ := ri["available_colors"].([]interface{})
		for _, col := range colors {
			if !strings.EqualFold(col.(string), "White") {
				t.Errorf("color filter leaked %v", col)
			}
		}
	}
}

// TestEverySingleFieldAxis: city + exactly ONE other field, for each field, every value — no crash, invariants hold.
func TestEverySingleFieldAxis(t *testing.T) {
	city := "Riyadh"
	axes := map[string][]string{
		"product":   {"STO", "MONTHLY", "STS"},
		"body_type": {"Sedan", "SUV", "Crossover", "CUV", "Hatchback", "Minivan", "Pickup", "Van"},
		"tier":      {"Entry level Sedan", "Midsize Sedan", "Midsize SUV", "Luxury", "Pickup Truck"},
		"condition": {"NEW", "USED"},
		"query":     {"Toyota", "Yaris", "Camry", "BMW", "Ferrari", "NonExistent"},
		"color":     {"White", "Black", "Silver", "Magenta"},
	}
	for field, vals := range axes {
		for _, v := range vals {
			pl := fmt.Sprintf(`{"city":%q,%q:%q}`, city, field, v)
			code, resp := doRequest(t, pl, TEST_API_KEY)
			if code != 200 {
				t.Errorf("%s=%s -> HTTP %d", field, v, code)
				continue
			}
			// invariants (no product/body/cond expectations unless that's the axis)
			rp, rb, rc := "", "", ""
			if field == "product" {
				rp = v
			}
			if field == "body_type" {
				rb = v
			}
			if field == "condition" {
				rc = v
			}
			if probs := invariantProblems(rp, city, rc, rb, 0, resp); len(probs) > 0 {
				t.Errorf("%s=%s violated: %v", field, v, probs)
			}
		}
	}
}
