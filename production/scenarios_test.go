package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A scenario is a realistic agent->service call. `said` is what the customer expressed;
// `payload` is what the agent would send to /search after understanding them.
type scenario struct {
	cat, name, said, payload string
}

// fireRaw posts a payload through the real handler and returns (code, parsed body).
func fireRaw(payload string) (int, map[string]interface{}) {
	req, _ := http.NewRequest("POST", "/search", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", TEST_API_KEY)
	rr := httptest.NewRecorder()
	SearchHandler(rr, req)
	var m map[string]interface{}
	_ = json.Unmarshal(rr.Body.Bytes(), &m)
	return rr.Code, m
}

func metaLine(resp map[string]interface{}) string {
	var parts []string
	for _, k := range []string{"target_body_type", "price_min", "price_max", "cheapest",
		"also_in_cities", "also_in_products", "available_body_types", "relaxed_filters"} {
		if v, ok := resp[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", k, v))
		}
	}
	return strings.Join(parts, " · ")
}

func resultsLines(resp map[string]interface{}) string {
	rs, _ := resp["results"].([]interface{})
	if len(rs) == 0 {
		return "      (no cars)\n"
	}
	var b strings.Builder
	for _, ri := range rs {
		c, _ := ri.(map[string]interface{})
		desc, _ := c["description"].(string)
		bp, _ := c["base_price"].(float64)
		body, _ := c["body_type"].(string)
		tier, _ := c["tier"].(string)
		prod, _ := c["product"].(string)
		basis, _ := c["price_basis"].(string)
		sf, _ := c["starter_fee"].(float64)
		extra := ""
		if strings.EqualFold(prod, "STO") && sf > 0 {
			extra = fmt.Sprintf(", starter %.0f", sf)
		}
		b.WriteString(fmt.Sprintf("      - %-26s %.0f SAR/%s%s  [%s / %s / %s]\n",
			desc, bp, basis, extra, body, tier, prod))
	}
	return b.String()
}

// scenarios — ~100 realistic cases, grouped. Many are derived directly from the 20 client calls.
var scenarios = []scenario{
	// ============ A. DERIVED FROM THE 20 REAL CLIENT CALLS ============
	{"Real calls", "Conv1 — Khobar STO (no stock there)", "Abdulrahman: ownership car in Al Khobar", `{"city":"Al Khobar","product":"STO"}`},
	{"Real calls", "Conv1 — Dammam STO (pivot city)", "...then 'I want Dammam'", `{"city":"Dammam","product":"STO"}`},
	{"Real calls", "Conv1 — JAC/GS3 family SUV Dammam", "picks the JAC JS3 / GS3 SUV", `{"city":"Dammam","product":"STO","body_type":"SUV"}`},
	{"Real calls", "Conv2 — Kia Sportage SUV Jeddah, NO budget", "Mohammed: Kia SUV, refuses budget", `{"city":"Jeddah","product":"STO","query":"Sportage","body_type":"SUV"}`},
	{"Real calls", "Conv2 — Tucson (we don't carry) no budget", "wants a Tucson, no budget", `{"city":"Jeddah","product":"STO","query":"Tucson"}`},
	{"Real calls", "Conv2 — 'two types' sedan Jeddah no budget", "just wants to browse sedans", `{"city":"Jeddah","product":"STO","body_type":"Sedan"}`},
	{"Real calls", "Conv2 — Corolla Jeddah no budget", "'Corolla is available?'", `{"city":"Jeddah","product":"STO","query":"Corolla"}`},
	{"Real calls", "Conv4 — Hyundai Monthly Jeddah ~5000", "Mahmoud: Hyundai, 5000/month, family", `{"city":"Jeddah","product":"MONTHLY","query":"Hyundai","max_price":5000}`},
	{"Real calls", "Conv4 — family car (→SUV) Monthly 5000", "city+family use, 5000/mo", `{"city":"Jeddah","product":"MONTHLY","body_type":"SUV","max_price":5000}`},
	{"Real calls", "Conv13 — wants to OWN, Riyadh", "Mazen: own a car (city later clarified)", `{"city":"Riyadh","product":"STO"}`},
	{"Real calls", "Conv18 — invalid city 'Kubo'", "Baraa: pickup in 'Kubo'", `{"city":"Kubo","product":"MONTHLY"}`},
	{"Real calls", "Conv7/9/20 — expat broad monthly Riyadh", "expat, vague, monthly", `{"city":"Riyadh","product":"MONTHLY"}`},
	{"Real calls", "Conv8 — Kia, then cancelled (pre-cancel search)", "Sultan: Kia (city Riyadh)", `{"city":"Riyadh","product":"STO","query":"Kia"}`},

	// ============ B. BROAD: plan × city (city+product only) ============
	{"Broad", "STO broad — Riyadh", "'show me ownership cars'", `{"city":"Riyadh","product":"STO"}`},
	{"Broad", "STO broad — Jeddah", "ownership cars in Jeddah", `{"city":"Jeddah","product":"STO"}`},
	{"Broad", "STO broad — Taif (no STO)", "ownership in Taif", `{"city":"Taif","product":"STO"}`},
	{"Broad", "MONTHLY broad — Riyadh", "monthly subscription, any car", `{"city":"Riyadh","product":"MONTHLY"}`},
	{"Broad", "MONTHLY broad — Mecca", "monthly in Mecca", `{"city":"Mecca","product":"MONTHLY"}`},
	{"Broad", "MONTHLY broad — Arar", "monthly in Arar", `{"city":"Arar","product":"MONTHLY"}`},
	{"Broad", "STS broad — Riyadh (weekly)", "short-term rental, any", `{"city":"Riyadh","product":"STS"}`},
	{"Broad", "STS broad — Madinah", "weekly rental in Madinah", `{"city":"Madinah","product":"STS"}`},
	{"Broad", "No product (cross-plan) — Riyadh", "agent forgot product", `{"city":"Riyadh"}`},

	// ============ C. NO-BUDGET (the A1+B7 fix — must never demand budget) ============
	{"No-budget", "STO SUV Riyadh, no budget", "'an SUV', won't say budget", `{"city":"Riyadh","product":"STO","body_type":"SUV"}`},
	{"No-budget", "MONTHLY Sedan Riyadh, no budget", "monthly sedan, no budget", `{"city":"Riyadh","product":"MONTHLY","body_type":"Sedan"}`},
	{"No-budget", "STO unknown model → Sedan ladder", "'a Mazda 6', no budget", `{"city":"Riyadh","product":"STO","query":"Mazda 6"}`},
	{"No-budget", "STO unknown model + SUV no budget", "'a Tahoe SUV', no budget", `{"city":"Riyadh","product":"STO","query":"Tahoe","body_type":"SUV"}`},
	{"No-budget", "MONTHLY SUV Jeddah no budget", "monthly SUV, browsing", `{"city":"Jeddah","product":"MONTHLY","body_type":"SUV"}`},
	{"No-budget", "STO Pickup no budget", "a pickup, no budget", `{"city":"Riyadh","product":"STO","body_type":"Pickup"}`},
	{"No-budget", "STO Hatchback no budget", "small hatchback, no budget", `{"city":"Riyadh","product":"STO","body_type":"Hatchback"}`},

	// ============ D. BUDGET variations ============
	{"Budget", "STO Sedan under 2000", "sedan, around 2000/mo", `{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":2000}`},
	{"Budget", "STO Sedan budget too low (700)", "sedan, only 700/mo", `{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":700}`},
	{"Budget", "STO SUV budget too low (300)", "SUV, 300/mo", `{"city":"Riyadh","product":"STO","body_type":"SUV","max_price":300}`},
	{"Budget", "STO SUV mid budget 2600", "SUV around 2600", `{"city":"Riyadh","product":"STO","body_type":"SUV","max_price":2600}`},
	{"Budget", "STO Sedan exactly 2300", "sedan, exactly 2300", `{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":2300}`},
	{"Budget", "STO range 1500–2500", "'between 1500 and 2500'", `{"city":"Riyadh","product":"STO","body_type":"Sedan","min_price":1500,"max_price":2500}`},
	{"Budget", "STO range 3000–4000 (premium)", "'between 3000 and 4000'", `{"city":"Riyadh","product":"STO","body_type":"SUV","min_price":3000,"max_price":4000}`},
	{"Budget", "STO band edge: just over budget", "sedan ~2400 (band test)", `{"city":"Riyadh","product":"STO","query":"Zzz","body_type":"Sedan","max_price":2400}`},
	{"Budget", "MONTHLY budget 1800", "monthly under 1800", `{"city":"Riyadh","product":"MONTHLY","body_type":"Sedan","max_price":1800}`},
	{"Budget", "Broad cheap (budget only, no body) 1500", "'anything under 1500'", `{"city":"Riyadh","product":"STO","max_price":1500}`},

	// ============ E. MIN_YEAR / MIN_PRICE ============
	{"Year/Floor", "STO 2025 or newer", "'2025 or newer'", `{"city":"Riyadh","product":"STO","min_year":2025}`},
	{"Year/Floor", "STO Sedan 2026 only", "brand new sedans only", `{"city":"Riyadh","product":"STO","body_type":"Sedan","min_year":2026}`},
	{"Year/Floor", "STO SUV 2024+ under 3000", "2024+ SUV under 3000", `{"city":"Riyadh","product":"STO","body_type":"SUV","min_year":2024,"max_price":3000}`},
	{"Year/Floor", "STO min_price 2500 floor", "'at least premium, 2500+'", `{"city":"Riyadh","product":"STO","min_price":2500}`},
	{"Year/Floor", "STO impossible year 2030", "'2030 or newer' (none)", `{"city":"Riyadh","product":"STO","body_type":"Sedan","min_year":2030}`},
	{"Year/Floor", "MONTHLY 2025+ SUV", "monthly SUV 2025+", `{"city":"Riyadh","product":"MONTHLY","body_type":"SUV","min_year":2025}`},

	// ============ F. CONDITION (soft) ============
	{"Condition", "STO USED sedan", "'a used sedan'", `{"city":"Riyadh","product":"STO","body_type":"Sedan","condition":"USED"}`},
	{"Condition", "STO NEW sedan", "'brand new sedan'", `{"city":"Riyadh","product":"STO","body_type":"Sedan","condition":"NEW"}`},
	{"Condition", "STO USED unknown model (soft fallback)", "'used Mazda 6'", `{"city":"Riyadh","product":"STO","query":"Mazda 6","body_type":"Sedan","condition":"USED","max_price":4000}`},
	{"Condition", "MONTHLY USED SUV", "'used SUV monthly'", `{"city":"Riyadh","product":"MONTHLY","body_type":"SUV","condition":"USED"}`},
	{"Condition", "STO USED + budget", "used sedan around 2000", `{"city":"Riyadh","product":"STO","body_type":"Sedan","condition":"USED","max_price":2000}`},

	// ============ G. COLOR ============
	{"Color", "STO white sedan", "'a white sedan'", `{"city":"Riyadh","product":"STO","body_type":"Sedan","color":"White"}`},
	{"Color", "STO black SUV", "'black SUV'", `{"city":"Riyadh","product":"STO","body_type":"SUV","color":"Black"}`},
	{"Color", "STO magenta (no such color)", "'a magenta car'", `{"city":"Riyadh","product":"STO","color":"Magenta"}`},
	{"Color", "STO white + budget", "white sedan under 2200", `{"city":"Riyadh","product":"STO","body_type":"Sedan","color":"White","max_price":2200}`},

	// ============ H. TIER / LUXURY ============
	{"Tier", "STO Midsize Sedan", "'a midsize sedan'", `{"city":"Riyadh","product":"STO","tier":"Midsize Sedan"}`},
	{"Tier", "STO Entry level SUV", "'a small/entry SUV'", `{"city":"Riyadh","product":"STO","tier":"Entry level SUV"}`},
	{"Tier", "STO Luxury (generic)", "'a luxury car'", `{"city":"Riyadh","product":"STO","tier":"Luxury"}`},
	{"Tier", "STO Luxury SUV", "'a luxury SUV'", `{"city":"Riyadh","product":"STO","tier":"Luxury SUV"}`},
	{"Tier", "STO Pickup Truck tier", "'a pickup truck'", `{"city":"Riyadh","product":"STO","tier":"Pickup Truck"}`},
	{"Tier", "MONTHLY Midsize Sedan + budget", "monthly midsize sedan 2500", `{"city":"Riyadh","product":"MONTHLY","tier":"Midsize Sedan","max_price":2500}`},

	// ============ I. SPECIFIC MODELS (exist vs not) ============
	{"Models", "STO Toyota (brand)", "'a Toyota'", `{"city":"Riyadh","product":"STO","query":"Toyota"}`},
	{"Models", "STO Yaris (exists)", "'a Yaris'", `{"city":"Riyadh","product":"STO","query":"Yaris"}`},
	{"Models", "STO Camry (maybe absent)", "'a Camry'", `{"city":"Riyadh","product":"STO","query":"Camry","max_price":3000}`},
	{"Models", "STO Creta (SUV)", "'a Hyundai Creta'", `{"city":"Riyadh","product":"STO","query":"Creta"}`},
	{"Models", "STO Ferrari (never) + sedan anchor", "'a Ferrari'", `{"city":"Riyadh","product":"STO","query":"Ferrari","body_type":"Sedan","max_price":2500}`},
	{"Models", "STO unknown brand, no anchor", "'a Zxqwerty'", `{"city":"Riyadh","product":"STO","query":"Zxqwerty"}`},
	{"Models", "STO MG 5 (exists)", "'MG 5'", `{"city":"Riyadh","product":"STO","query":"MG 5"}`},
	{"Models", "MONTHLY Kia Seltos", "'Kia Seltos monthly'", `{"city":"Riyadh","product":"MONTHLY","query":"Seltos"}`},

	// ============ J. BODY INFERENCE (family / jeep) ============
	{"Body-infer", "Family car → SUV (STO)", "'a family car'", `{"city":"Riyadh","product":"STO","body_type":"SUV"}`},
	{"Body-infer", "Jeep (Saudi=SUV) → SUV", "'a jeep' (means SUV)", `{"city":"Riyadh","product":"STO","body_type":"SUV"}`},
	{"Body-infer", "Crossover request", "'a crossover'", `{"city":"Riyadh","product":"STO","body_type":"Crossover"}`},
	{"Body-infer", "Minivan request", "'a minivan / 7-seater'", `{"city":"Riyadh","product":"STO","body_type":"Minivan"}`},

	// ============ K. DEAD-END candidates (must carry hints) ============
	{"Dead-end", "Invalid city (Atlantis)", "garbled/unknown city", `{"city":"Atlantis","product":"STO"}`},
	{"Dead-end", "Van in Riyadh (no vans)", "'a Van'", `{"city":"Riyadh","product":"STO","body_type":"Van"}`},
	{"Dead-end", "STO in Turayf (sparse)", "ownership in Turayf", `{"city":"Turayf","product":"STO"}`},
	{"Dead-end", "Luxury in a small city", "luxury in Arar", `{"city":"Arar","product":"STO","tier":"Luxury"}`},
	{"Dead-end", "Pickup Monthly Mecca", "monthly pickup in Mecca", `{"city":"Mecca","product":"MONTHLY","body_type":"Pickup"}`},

	// ============ L. STS specifics (weekly, no band, no budget-needed) ============
	{"STS", "STS SUV no budget", "weekly SUV, no budget", `{"city":"Riyadh","product":"STS","body_type":"SUV"}`},
	{"STS", "STS Sedan weekly budget 500", "weekly sedan ~500", `{"city":"Riyadh","product":"STS","body_type":"Sedan","max_price":500}`},
	{"STS", "STS unknown model → anchor", "'a Tucson weekly'", `{"city":"Riyadh","product":"STS","query":"Tucson"}`},
	{"STS", "STS specific model Yaris", "'Yaris for a week'", `{"city":"Riyadh","product":"STS","query":"Yaris"}`},
	{"STS", "STS Jeddah broad", "weekly any, Jeddah", `{"city":"Jeddah","product":"STS"}`},
	{"STS", "STS in a city with no STS", "weekly in Arar", `{"city":"Arar","product":"STS"}`},

	// ============ M. CROSS city / product (inform-only) ============
	{"Cross", "STO model that lives in another city", "'a Geely Emgrand' (STO sparse)", `{"city":"Dammam","product":"STO","query":"Emgrand"}`},
	{"Cross", "STO body absent here but elsewhere", "Pickup STO Jeddah", `{"city":"Jeddah","product":"STO","body_type":"Pickup"}`},
	{"Cross", "Luxury that exists on another product", "luxury STO (maybe monthly-only)", `{"city":"Riyadh","product":"STO","tier":"Luxury SUV"}`},
	{"Cross", "Model only on Monthly, asked on STO", "'X70' on STO", `{"city":"Riyadh","product":"STO","query":"X70"}`},

	// ============ N. ROBUSTNESS (messy but valid payloads) ============
	{"Robust", "Case/space tolerance", "' riYAdh ' / 'sto' / 'tOyOtA'", `{"city":" riYAdh ","product":"sto","query":"tOyOtA"}`},
	{"Robust", "max_price as string", "budget '2,500'", `{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":"2,500"}`},
	{"Robust", "min_year as string", "'2024'", `{"city":"Riyadh","product":"STO","body_type":"Sedan","min_year":"2024"}`},
	{"Robust", "null/empty fields", "everything blank but city", `{"city":"Riyadh","product":"STO","query":"","tier":"","color":"","condition":"","max_price":null}`},
	{"Robust", "body type in query (STT slip)", "'SUV' put in query", `{"city":"Riyadh","product":"STO","query":"SUV"}`},

	// ============ O. MORE REAL-CUSTOMER MESSINESS (how the 20 callers actually talked) ============
	{"Real-messy", "Garbled brand 'Sony' (Conv1 mis-hear)", "customer/STT says a 'Sony' car", `{"city":"Riyadh","product":"STO","query":"Sony"}`},
	{"Real-messy", "Luxury brand we don't carry — BMW", "'show me BMW'", `{"city":"Riyadh","product":"STO","query":"BMW"}`},
	{"Real-messy", "Luxury brand — Mercedes (Monthly)", "'a Mercedes'", `{"city":"Riyadh","product":"MONTHLY","query":"Mercedes"}`},
	{"Real-messy", "Brand-only vague — Toyota", "'just a Toyota'", `{"city":"Riyadh","product":"STO","query":"Toyota"}`},
	{"Real-messy", "Brand-only vague — Hyundai (Monthly)", "'something Hyundai'", `{"city":"Jeddah","product":"MONTHLY","query":"Hyundai"}`},
	{"Real-messy", "Wrong-brand model 'Kia Tucson'", "Conv2: 'Kia Tucson' (Tucson is Hyundai)", `{"city":"Jeddah","product":"STO","query":"Kia Tucson","body_type":"SUV"}`},
	{"Real-messy", "Model + too-low budget (Creta @1500)", "'a Creta, around 1500'", `{"city":"Riyadh","product":"STO","query":"Creta","max_price":1500}`},
	{"Real-messy", "Weekly budget sent as monthly (600)", "'600 a week' -> agent sends STO 600", `{"city":"Riyadh","product":"STO","body_type":"Sedan","max_price":600}`},
	{"Real-messy", "'cheapest you have'", "'just the cheapest car'", `{"city":"Riyadh","product":"STO","max_price":99999}`},
	{"Real-messy", "'anything available' (no plan)", "totally vague, no plan yet", `{"city":"Jeddah"}`},
	{"Real-messy", "Compare step A — Yaris", "Conv2 compare: Yaris…", `{"city":"Jeddah","product":"STO","query":"Yaris"}`},
	{"Real-messy", "Compare step B — Sunny (absent)", "Conv2 compare: …vs Sunny", `{"city":"Jeddah","product":"STO","query":"Sunny"}`},
	{"Real-messy", "7-seater for the family", "'big car for 7 people'", `{"city":"Riyadh","product":"MONTHLY","body_type":"Minivan"}`},
	{"Real-messy", "Uber/Careem driver wants cheap monthly", "'a car for Uber, cheap'", `{"city":"Riyadh","product":"MONTHLY","body_type":"Sedan","max_price":1800}`},
}

// TestScenarioMatrix fires every scenario, writes a full report, and asserts the
// production-critical invariants on realistic, conversation-derived payloads.
func TestScenarioMatrix(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Invygo `/search` — Scenario Matrix (payload → response)\n\n")
	b.WriteString("Generated by `go test -run TestScenarioMatrix`. Payloads reflect how the 20 real campaign\n")
	b.WriteString("callers actually spoke — vague/browsing, refusing budget, naming cars we don't carry, cities\n")
	b.WriteString("with no stock for the plan, garbled brands.\n\n")
	b.WriteString("**Realism key.** High-frequency real behaviour: `Real calls`, `Real-messy`, `No-budget`,\n")
	b.WriteString("`Broad`, `Models`, `Body-infer`, `Dead-end` (valid city, no stock). Feature coverage\n")
	b.WriteString("(customers rarely speak this precisely, but the params exist): `Year/Floor`, `Color`, `Tier`,\n")
	b.WriteString("range budgets. Robustness only — the tool's `city` **enum of 9** normally prevents these\n")
	b.WriteString("from ever reaching the service: the `Atlantis`/`Kubo` cases.\n\n")

	statusCount := map[string]int{}
	catOrder := []string{}
	seenCat := map[string]bool{}
	var bodyByCat = map[string]*strings.Builder{}

	for _, s := range scenarios {
		if !seenCat[s.cat] {
			seenCat[s.cat] = true
			catOrder = append(catOrder, s.cat)
			bodyByCat[s.cat] = &strings.Builder{}
		}
		code, resp := fireRaw(s.payload)
		status, _ := resp["status"].(string)
		msg, _ := resp["message"].(string)
		statusCount[status]++

		sb := bodyByCat[s.cat]
		sb.WriteString(fmt.Sprintf("### %s\n", s.name))
		sb.WriteString(fmt.Sprintf("- **Customer:** %s\n", s.said))
		sb.WriteString(fmt.Sprintf("- **Payload:** `%s`\n", s.payload))
		sb.WriteString(fmt.Sprintf("- **HTTP:** %d  **status:** `%s`\n", code, status))
		sb.WriteString(fmt.Sprintf("- **message:** %s\n", msg))
		if m := metaLine(resp); m != "" {
			sb.WriteString(fmt.Sprintf("- **hints:** %s\n", m))
		}
		sb.WriteString("- **results:**\n```\n")
		sb.WriteString(resultsLines(resp))
		sb.WriteString("```\n\n")

		// ---- production-critical invariants (recorded, non-fatal so report still writes) ----
		rs, _ := resp["results"].([]interface{})
		if status == "Budget Needed" {
			t.Errorf("[%s] retired status 'Budget Needed' returned for %s", s.name, s.payload)
		}
		if len(rs) == 0 && status != StatusError && status != StatusNeedBodyType {
			_, c1 := resp["also_in_cities"]
			_, c2 := resp["also_in_products"]
			_, c3 := resp["available_body_types"]
			if !c1 && !c2 && !c3 {
				t.Errorf("[%s] empty results with NO alternative hint (dead-end) for %s", s.name, s.payload)
			}
		}
		if len(rs) > 4 {
			t.Errorf("[%s] returned %d results (>4) for %s", s.name, len(rs), s.payload)
		}
	}

	for _, cat := range catOrder {
		b.WriteString(fmt.Sprintf("## %s\n\n", cat))
		b.WriteString(bodyByCat[cat].String())
	}

	// Summary
	var sum strings.Builder
	sum.WriteString("## Summary\n\n")
	sum.WriteString(fmt.Sprintf("- Total scenarios: **%d**\n", len(scenarios)))
	keys := make([]string, 0, len(statusCount))
	for k := range statusCount {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sum.WriteString(fmt.Sprintf("  - `%s`: %d\n", k, statusCount[k]))
	}
	if _, ok := statusCount["Budget Needed"]; ok {
		sum.WriteString("  - ⚠️ Budget Needed should be 0 (retired)\n")
	} else {
		sum.WriteString("  - ✅ no `Budget Needed` (retired) anywhere\n")
	}

	report := "# Invygo `/search` — Scenario Matrix (payload → response)\n\n" + sum.String() + "\n" + b.String()[len("# Invygo `/search` — Scenario Matrix (payload → response)\n\n"):]

	out := filepath.Join("testdata", "scenario_report.md")
	if err := os.WriteFile(out, []byte(report), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	t.Logf("wrote %s (%d scenarios)", out, len(scenarios))
	t.Logf("status distribution: %v", statusCount)
}
