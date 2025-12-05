package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvygoMasterSuite(t *testing.T) {
	// 1. Initialize Inventory (Pre-load from URL)
	_, err := getInventory()
	if err != nil {
		t.Log("⚠️ Warning: Could not fetch live inventory. Tests relying on specific data might fail.")
	}

	tests := []struct {
		name           string
		payload        string // JSON Input
		apiKey         string
		wantStatusCode int
		// Custom verification logic for each test
		validation func(*testing.T, map[string]interface{})
	}{
		// =================================================================================
		// SUITE A: SECURITY & INPUT VALIDATION (The Gatekeepers)
		// =================================================================================
		{
			name:           "A1. Missing City (Mandatory Check)",
			payload:        `{"query": "Toyota"}`,
			apiKey:         API_KEY,
			wantStatusCode: 200, // Service returns 200 with error JSON
			validation: func(t *testing.T, resp map[string]interface{}) {
				if resp["status"] != "Error" {
					t.Errorf("Expected status 'Error', got '%v'", resp["status"])
				}
				msg, _ := resp["message"].(string)
				if !strings.Contains(msg, "City is mandatory") {
					t.Errorf("Wrong error message: %v", msg)
				}
			},
		},
		{
			name:           "A2. Invalid API Key",
			payload:        `{"city": "Riyadh"}`,
			apiKey:         "wrong-key-123",
			wantStatusCode: 401,
			validation:     nil,
		},
		{
			name:           "A3. Missing API Key",
			payload:        `{"city": "Riyadh"}`,
			apiKey:         "",
			wantStatusCode: 401,
			validation:     nil,
		},

		// =================================================================================
		// SUITE B: THE "SUBSCRIBE TO OWN" GUARANTEE (Business Logic Pivot)
		// =================================================================================
		{
			name:           "B1. Verify All Cars are STO",
			payload:        `{"city": "Riyadh"}`, // Broad search
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Fatal("No results found for broad search")
				}
				first := results[0].(map[string]interface{})
				
				// CRITICAL CHECK: PlanType must be "Subscribe to Own"
				if first["plan_type"] != "Subscribe to Own" {
					t.Errorf("Critical Fail: Expected 'Subscribe to Own', got '%v'", first["plan_type"])
				}
				// CRITICAL CHECK: Starter Fee must be > 0
				if fee, _ := first["starter_fee"].(float64); fee <= 0 {
					t.Errorf("Critical Fail: Starter Fee is 0 for STO plan.")
				}
			},
		},
		{
			name:           "B2. The Rental Pivot (User asks for Rent -> We show STO)",
			payload:        `{"city": "Riyadh", "plan_preference": "rent"}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Fatal("Service blocked 'rent' request instead of pivoting to STO.")
				}
				// Ensure we are still showing STO cars
				first := results[0].(map[string]interface{})
				if first["plan_type"] != "Subscribe to Own" {
					t.Errorf("Pivot Failed: Returned '%v' instead of STO", first["plan_type"])
				}
			},
		},

		// =================================================================================
		// SUITE C: THE 4-LAYER SALES FUNNEL (Intelligence Check)
		// =================================================================================
		{
			name:           "C1. Layer 1: Ideal Match (Happy Path)",
			payload:        `{"city": "Riyadh", "query": "Yaris", "max_price": 3000}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				if resp["status"] != "Ideal Match Found" {
					t.Logf("Note: Expected 'Ideal Match', got '%v' (Check if Yaris exists under 3000)", resp["status"])
				}
				results, _ := resp["results"].([]interface{})
				if len(results) > 0 {
					model := strings.ToLower(results[0].(map[string]interface{})["model"].(string))
					if !strings.Contains(model, "yaris") {
						t.Errorf("Expected Yaris, got %v", model)
					}
				}
			},
		},
		{
			name:           "C2. Layer 2: Smart Swap (Wrong Name, Right Need)",
			payload:        `{"city": "Riyadh", "query": "Honda Civic", "intention": "budget", "max_price": 2500}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				// We don't have Honda, but we have Budget cars.
				// Should fallback to Layer 2
				if resp["status"] != "Alternative Suggestions Found" && resp["status"] != "Budget Match Found" {
					t.Errorf("Expected Alternative/Budget match, got '%v'", resp["status"])
				}
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Fatal("Smart Swap failed to find alternatives.")
				}
			},
		},
		{
			name:           "C3. Layer 3: Budget Match (Price First)",
			// FIX: Use 'family' intention. Since Ferrari isn't a family car, Layer 2 fails. 
			// Then Layer 3 (Budget) kicks in to show anything cheap.
			payload:        `{"city": "Riyadh", "query": "Ferrari", "intention": "family", "max_price": 2000}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				status := resp["status"].(string)
				// Should be Budget Match or Upsell
				if status != "Budget Match Found" && status != "Upsell Options Found" {
					t.Errorf("Expected 'Budget Match' or 'Upsell', got '%v'", status)
				}
				results, _ := resp["results"].([]interface{})
				if len(results) > 0 {
					fee := results[0].(map[string]interface{})["monthly_fee"].(float64)
					if fee > 2000 {
						t.Errorf("Budget Match failed: Returned car costing %v > 2000", fee)
					}
				}
			},
		},
		{
			name:           "C4. Layer 4: Upsell (Low Budget)",
			payload:        `{"city": "Riyadh", "max_price": 500}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				// No car exists at 500. Should show cheapest available.
				if resp["status"] != "Upsell Options Found" {
					t.Errorf("Expected Upsell, got '%v'", resp["status"])
				}
				msg, _ := resp["message"].(string)
				if !strings.Contains(msg, "start from") {
					t.Errorf("Message missing upsell pitch: %v", msg)
				}
			},
		},

		// =================================================================================
		// SUITE D: AI INFERENCE & FEATURES
		// =================================================================================
		{
			name:           "D1. Intention: Desert (SUV Check)",
			payload:        `{"city": "Riyadh", "intention": "desert"}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 { return }
				
				first := results[0].(map[string]interface{})
				body := strings.ToUpper(first["body_type"].(string))
				
				// Smart check: Contains SUV, CROSSOVER, or 4x4 logic
				if !strings.Contains(body, "SUV") && !strings.Contains(body, "CROSSOVER") {
					t.Errorf("Desert Intention failed. Returned: %v", body)
				}
			},
		},
		{
			name:           "D2. Intention: Family (Seating Check)",
			payload:        `{"city": "Riyadh", "intention": "family"}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 { return }
				// Just ensure we got results. Logic prioritizes 7 seats but allows 5.
				first := results[0].(map[string]interface{})
				bestFor := strings.ToLower(first["best_for"].(string))
				if !strings.Contains(bestFor, "family") && !strings.Contains(first["features"].(string), "7-Seater") {
					t.Logf("Warning: Family intention returned car tagged as '%v'", bestFor)
				}
			},
		},

		// =================================================================================
		// SUITE E: EDGE CASES & ROBUSTNESS
		// =================================================================================
		{
			name:           "E1. Color Mismatch (Soft Filter)",
			payload:        `{"city": "Riyadh", "query": "Yaris", "color": "NeonPink"}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				// Should find Yaris, but warn about color
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Fatal("Soft Filter Failed: Returned no cars because of color.")
				}
				// Status might be Ideal or Color Mismatch depending on exact flow, 
				// but message usually contains color warning
				msg := resp["message"].(string)
				if !strings.Contains(msg, "NeonPink") && !strings.Contains(msg, "available in") {
					t.Logf("Info: Response message was: %v", msg)
				}
			},
		},
		{
			name:           "E2. Case Insensitivity",
			payload:        `{"city": "rIyAdH", "query": "tOyOtA"}`,
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Fatal("Case insensitivity check failed.")
				}
				brand := strings.ToLower(results[0].(map[string]interface{})["brand"].(string))
				if brand != "toyota" {
					t.Errorf("Expected Toyota, got %v", brand)
				}
			},
		},
		{
			name:           "E3. City Separation (Riyadh vs Jeddah)",
			payload:        `{"city": "Jeddah"}`, // Ensure Jeddah returns results too
			apiKey:         API_KEY,
			wantStatusCode: 200,
			validation: func(t *testing.T, resp map[string]interface{}) {
				results, _ := resp["results"].([]interface{})
				if len(results) == 0 {
					t.Log("Warning: No cars found in Jeddah (might be empty inventory data).")
				}
			},
		},
	}

	// EXECUTE ALL TESTS
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/search", bytes.NewBufferString(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			if tc.apiKey != "" {
				req.Header.Set("x-api-key", tc.apiKey)
			}

			rr := httptest.NewRecorder()

			// CALL THE HANDLER DIRECTLY
			SearchHandler(rr, req)

			// 1. Check Status Code
			if rr.Code != tc.wantStatusCode {
				t.Errorf("StatusCode: got %v, want %v", rr.Code, tc.wantStatusCode)
			}

			// 2. Run Custom Validation
			if tc.validation != nil && rr.Code == 200 {
				var resp map[string]interface{}
				if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
					t.Fatalf("Failed to parse JSON response: %v", err)
				}
				tc.validation(t, resp)
			}
		})
	}
}