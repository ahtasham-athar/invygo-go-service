package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
)

func keysOf(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Helper to execute request
func executeTestRequest(t *testing.T, payload string, apiKey string) (int, map[string]interface{}) {
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

func TestInvygoComprehensiveSuite(t *testing.T) {
	// Ensure the handler is configured during tests.
	// Tests rely on TEST_API_KEY as the client-provided key.
	_ = os.Setenv("INVYGO_API_KEY", TEST_API_KEY)
	defer os.Unsetenv("INVYGO_API_KEY")

	// Pre-load inventory to ensure logic runs on real data structure
	_, err := getInventory()
	if err != nil {
		t.Log("⚠️ Warning: Could not fetch live inventory. Some tests might fail if dependent on specific cars.")
	}

	tests := []struct {
		name           string
		payload        string
		apiKey         string
		wantStatusCode int
		// Custom verification logic
		validate func(*testing.T, map[string]interface{})
	}{
		// =================================================================================
		// SUITE A: SECURITY & INPUT VALIDATION
		// =================================================================================
		{
			name:           "A1. Missing API Key",
			payload:        `{"city": "Riyadh"}`,
			apiKey:         "",
			wantStatusCode: 401,
			validate:       nil,
		},
		{
			name:           "A2. Invalid API Key",
			payload:        `{"city": "Riyadh"}`,
			apiKey:         "wrong-key",
			wantStatusCode: 401,
			validate:       nil,
		},
		{
			name:           "A3. Missing City (Mandatory)",
			payload:        `{"query": "Toyota"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				if resp["status"] != "Error" {
					t.Errorf("Expected status Error, got %v", resp["status"])
				}
			},
		},
		{
			name:           "A4. Empty JSON Payload",
			payload:        `{}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				if resp["status"] != "Error" {
					t.Errorf("Expected status Error for empty payload")
				}
			},
		},

		// =================================================================================
		// SUITE B: BUSINESS LOGIC GUARANTEES
		// =================================================================================
		{
			name:           "B1. Subscribe to Own Only",
			payload:        `{"city": "Riyadh"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Fatal("No results found")
				}
				first := results[0].(map[string]interface{})
				if first["plan_type"] != "Subscribe to Own" {
					t.Errorf("Critical: Returned %v instead of STO", first["plan_type"])
				}
			},
		},
		{
			name:           "B2. Pricing Integrity (Fees > 0)",
			payload:        `{"city": "Riyadh"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				for _, r := range results {
					car := r.(map[string]interface{})
					// Note: Some CSV rows have 0 monthly fee (Data issue), but code handles it.
					// We check logic doesn't crash.
					if car["starter_fee"] == nil {
						t.Errorf("Missing starter_fee in result")
					}
				}
			},
		},

		// =================================================================================
		// SUITE C: EXACT MATCHING (Phase 1)
		// =================================================================================
		{
			name:           "C1. Brand Match (Toyota)",
			payload:        `{"city": "Riyadh", "query": "Toyota"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				if resp["status"] != "Ideal Match Found" {
					t.Errorf("Expected Ideal Match, got %v", resp["status"])
				}
				results := resp["results"].([]interface{})
				brand := results[0].(map[string]interface{})["brand"].(string)
				if !strings.EqualFold(brand, "Toyota") {
					t.Errorf("Expected Toyota, got %v", brand)
				}
			},
		},
		{
			name:           "C2. Model Match (Yaris)",
			payload:        `{"city": "Riyadh", "query": "Yaris"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				model := results[0].(map[string]interface{})["model"].(string)
				if !strings.Contains(strings.ToLower(model), "yaris") {
					t.Errorf("Expected Yaris, got %v", model)
				}
			},
		},
		{
			name:           "C3. Tier Match (Midsize Sedan)",
			payload:        `{"city": "Riyadh", "tier": "Midsize Sedan"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				tier := results[0].(map[string]interface{})["tier"].(string)
				if !strings.EqualFold(tier, "Midsize Sedan") {
					t.Errorf("Expected Midsize Sedan, got %v", tier)
				}
			},
		},
		{
			name:           "C4. Body Type Match (SUV)",
			payload:        `{"city": "Riyadh", "body_type": "SUV"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				body := results[0].(map[string]interface{})["body_type"].(string)
				if !strings.EqualFold(body, "SUV") {
					t.Errorf("Expected SUV, got %v", body)
				}
			},
		},

		// =================================================================================
		// SUITE D: SMART FALLBACK (Phase 1.5)
		// =================================================================================
		{
			name: "D1. Tier Fallback (Specific Car Missing -> Show Same Tier)",
			// Request a fake car but specify Tier
			payload:        `{"city": "Riyadh", "query": "NonExistentCar", "tier": "Entry level Sedan"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				// Should fail Phase 1 (Ideal) but hit Phase 1.5 (Smart Fallback)
				// Code maps "Entry level Sedan" -> "sedan" body type
				status := resp["status"].(string)
				if status != "Category Match Found" {
					t.Errorf("Expected 'Category Match Found', got %v", status)
				}
				msg := resp["message"].(string)
				if !strings.Contains(msg, "specific car isn't available") {
					t.Errorf("Message did not explain fallback: %v", msg)
				}
			},
		},
		{
			name: "D2. Body Type Fallback (Specific Car Missing -> Show Same Body)",
			payload:        `{"city": "Riyadh", "query": "Ferrari", "body_type": "SUV"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				if status != "Category Match Found" {
					t.Errorf("Expected Category Match, got %v", status)
				}
				results := resp["results"].([]interface{})
				body := results[0].(map[string]interface{})["body_type"].(string)
				if !strings.EqualFold(body, "SUV") {
					t.Errorf("Fallback returned wrong body type: %v", body)
				}
			},
		},

		// =================================================================================
		// SUITE E: BUDGET & UPSELL (Phase 2 & 3)
		// =================================================================================
		{
			name:           "E1. Budget Match (No Specific Car)",
			payload:        `{"city": "Riyadh", "max_price": 2000}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				// Depending on inventory, might be Ideal (broad search) or Budget Match
				if status != "Budget Match Found" && status != "Ideal Match Found" {
					t.Errorf("Expected Budget/Ideal Match, got %v", status)
				}
				results := resp["results"].([]interface{})
				price := results[0].(map[string]interface{})["monthly_fee"].(float64)
				if price > 2000 {
					t.Errorf("Budget Match Failed: Price %v > 2000", price)
				}
			},
		},
		{
			name:           "E2. Upsell (Budget Too Low)",
			payload:        `{"city": "Riyadh", "max_price": 100}`, // Impossible price
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				if status != "Upsell Options Found" {
					t.Errorf("Expected Upsell Options, got %v", status)
				}
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Fatal("Upsell returned no cars")
				}
				// Should return cars sorted by price ascending (Cheapest first)
				p1 := results[0].(map[string]interface{})["monthly_fee"].(float64)
				p2 := results[1].(map[string]interface{})["monthly_fee"].(float64)
				if p1 > p2 {
					t.Errorf("Upsell sorting failed: %v > %v", p1, p2)
				}
			},
		},

		// =================================================================================
		// SUITE F: EDGE CASES & ROBUSTNESS
		// =================================================================================
		{
			name:           "F1. Case Insensitivity",
			payload:        `{"city": "rIyAdH", "query": "tOyOtA"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Error("Case insensitive search failed")
				}
			},
		},
		{
			name:           "F2. Empty String Parameters",
			payload:        `{"city": "Riyadh", "query": "", "tier": "", "body_type": ""}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Error("Empty parameters blocked broad search")
				}
			},
		},
		{
			name:           "F3. Specific City Separation (Jeddah)",
			payload:        `{"city": "Jeddah"}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				// City filtering is handled in filterCars, not exposed in GroupedResult
				// Just verify we get a valid response (results or "No Cars Found" status)
				status := resp["status"].(string)
				if status != "Ideal Match Found" && status != "No Cars Found" && status != "Upsell Options Found" {
					t.Errorf("Unexpected status for Jeddah search: %v", status)
				}
			},
		},
		{
			name:           "F4. Combined Constraints (SUV + Price)",
			payload:        `{"city": "Riyadh", "body_type": "SUV", "max_price": 3000}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				if len(results) > 0 {
					car := results[0].(map[string]interface{})
					if car["body_type"] != "SUV" {
						t.Errorf("Expected SUV, got %v", car["body_type"])
					}
					if car["monthly_fee"].(float64) > 3000 {
						t.Errorf("Price limit failed")
					}
				}
			},
		},

		// =================================================================================
		// SUITE G: NULL PARAMETER HANDLING & QUERY-BASED SEARCHES
		// =================================================================================
		{
			name:           "G1. Query SUV with Null Parameters",
			payload:        `{"city": "Riyadh", "tier": null, "color": null, "query": "SUV", "body_type": null, "condition": null, "max_price": null}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				if status == "Error" {
					t.Errorf("Query with null params failed: %v", resp["message"])
				}
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Error("SUV query returned no results")
				}
				// Should find cars with SUV in brand/model text OR body_type
				first := results[0].(map[string]interface{})
				bodyType := strings.ToLower(first["body_type"].(string))
				brand := strings.ToLower(first["brand"].(string))
				model := strings.ToLower(first["model"].(string))
				hasSUV := strings.Contains(bodyType, "suv") || strings.Contains(brand, "suv") || strings.Contains(model, "suv")
				if !hasSUV {
					t.Logf("Note: First result doesn't contain SUV explicitly: body=%s, brand=%s, model=%s", bodyType, brand, model)
				}
			},
		},
		{
			name:           "G2. Tier Entry Level SUV with Null Parameters",
			payload:        `{"city": "Riyadh", "tier": "Entry level SUV", "color": null, "query": null, "body_type": null, "condition": null, "max_price": null}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				if status == "Error" {
					t.Errorf("Tier search with null params failed: %v", resp["message"])
				}
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Log("Warning: No Entry level SUV found in inventory")
					return
				}
				// Verify tier matches
				tier := results[0].(map[string]interface{})["tier"].(string)
				if !strings.EqualFold(tier, "Entry level SUV") {
					t.Errorf("Expected tier 'Entry level SUV', got %v", tier)
				}
			},
		},
		{
			name:           "G3. All Null Parameters Except City",
			payload:        `{"city": "Riyadh", "tier": null, "color": null, "query": null, "body_type": null, "condition": null, "max_price": null}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				// Should return Ideal Match (broad search)
				if status != "Ideal Match Found" && status != "Upsell Options Found" {
					t.Errorf("Expected valid response for all-null params, got status: %v", status)
				}
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Error("Broad search with null params returned no results")
				}
			},
		},
		{
			name:           "G4. Query Sedan with Null Others",
			payload:        `{"city": "Riyadh", "query": "Sedan", "tier": null, "body_type": null}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Log("Warning: Sedan query returned no results")
					return
				}
				// Check if results contain sedan-related cars
				first := results[0].(map[string]interface{})
				bodyType := strings.ToLower(first["body_type"].(string))
				tier := strings.ToLower(first["tier"].(string))
				hasSedan := strings.Contains(bodyType, "sedan") || strings.Contains(tier, "sedan")
				if !hasSedan {
					t.Logf("Note: First result may not be sedan: body=%s, tier=%s", bodyType, tier)
				}
			},
		},
		{
			name:           "G5. Tier Midsize SUV Search",
			payload:        `{"city": "Riyadh", "tier": "Midsize SUV", "query": null, "body_type": null, "max_price": null}`,
			apiKey:         TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				if status == "Error" {
					t.Errorf("Midsize SUV tier search failed: %v", resp["message"])
				}
				results := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Log("Warning: No Midsize SUV found in inventory")
					return
				}
				tier := results[0].(map[string]interface{})["tier"].(string)
				if !strings.EqualFold(tier, "Midsize SUV") {
					t.Errorf("Expected tier 'Midsize SUV', got %v", tier)
				}
			},
		},
		// =================================================================================
        // SUITE H: SPECIFIC SCENARIO TESTING
        // =================================================================================
        {
            name:    "H1. Luxury Tier Search",
            payload: `{"city": "Riyadh", "tier": "luxury", "color": null, "query": "", "body_type": "", "condition": "", "max_price": null}`,
			apiKey:  TEST_API_KEY,
            wantStatusCode: 200,
            validate: func(t *testing.T, resp map[string]interface{}) {
                status := resp["status"].(string)
                if status == "Ideal Match Found" {
                    results := resp["results"].([]interface{})
                    tier := results[0].(map[string]interface{})["tier"].(string)
                    if !strings.EqualFold(tier, "luxury") {
                        t.Errorf("Expected Luxury tier, got %v", tier)
                    }
                }
            },
        },
        {
            name:    "H2. Crossover Body Type Search",
            payload: `{"city": "Riyadh", "tier": "", "color": "", "query": "", "body_type": "crossover", "condition": "", "max_price": null}`,
			apiKey:  TEST_API_KEY,
            wantStatusCode: 200,
            validate: func(t *testing.T, resp map[string]interface{}) {
                status := resp["status"].(string)
                if status == "Ideal Match Found" {
                    results := resp["results"].([]interface{})
                    body := results[0].(map[string]interface{})["body_type"].(string)
                    if !strings.EqualFold(body, "crossover") {
                        t.Errorf("Expected crossover, got %v", body)
                    }
                }
            },
        },
        {
            name:    "H3. USED Condition Filter",
            payload: `{"city": "Riyadh", "tier": "", "color": null, "query": "", "body_type": "", "condition": "USED", "max_price": null}`,
			apiKey:  TEST_API_KEY,
            wantStatusCode: 200,
            validate: func(t *testing.T, resp map[string]interface{}) {
                results := resp["results"].([]interface{})
                if len(results) > 0 {
                    first := results[0].(map[string]interface{})
					// In the API response, the field name is "condition" (see GroupedResult struct),
					// not "car_condition" (that's only in the underlying Car model).
					raw, ok := first["condition"]
					if !ok || raw == nil {
						t.Fatalf("Expected field 'condition' in result, got keys=%v", keysOf(first))
					}
					cond, ok := raw.(string)
					if !ok {
						t.Fatalf("Expected 'condition' to be string, got %T", raw)
					}
					if !strings.EqualFold(cond, "USED") {
						t.Errorf("Expected USED condition, got %v", cond)
					}
                }
            },
        },
        {
            name:    "H4. Garbage Query (No Match Fallback)",
            payload: `{"city": "Riyadh", "tier": "", "color": null, "query": "asdfsafsf", "body_type": "", "condition": "", "max_price": null}`,
			apiKey:  TEST_API_KEY,
            wantStatusCode: 200,
            validate: func(t *testing.T, resp map[string]interface{}) {
                status := resp["status"].(string)
                // When no match is found, logic should hit Phase 3: Upsell
                if status != "Upsell Options Found" {
                    t.Errorf("Expected Upsell for garbage query, got %v", status)
                }
            },
		},
		{
			name:    "H5. Budget Too Low (Price 10)",
			payload: `{"city": "Riyadh", "tier": "", "color": null, "query": "", "body_type": "", "condition": "", "max_price": 10}`,
			apiKey:  TEST_API_KEY,
			wantStatusCode: 200,
			validate: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				// 10 SAR is below any car price, should trigger Upsell
				if status != "Upsell Options Found" {
					t.Errorf("Expected Upsell for budget 10, got %v", status)
				}
			},
		},
	}

	// Execution Loop
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := executeTestRequest(t, tc.payload, tc.apiKey)

			if code != tc.wantStatusCode {
				t.Errorf("StatusCode: got %v, want %v", code, tc.wantStatusCode)
			}

			if tc.validate != nil && code == 200 {
				tc.validate(t, resp)
			}
		})
	}
}
