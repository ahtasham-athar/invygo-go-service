package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type recordedResponse struct {
	Name           string                 `json:"name"`
	Payload        json.RawMessage        `json:"payload"`
	APIKeyProvided bool                   `json:"api_key_provided"`
	StatusCode     int                    `json:"status_code"`
	Body           map[string]interface{} `json:"body,omitempty"`
	RawBody        string                 `json:"raw_body"`
	RecordedAt     time.Time              `json:"recorded_at"`
}

func TestRecordAllResponses(t *testing.T) {
	_ = os.Setenv("INVYGO_API_KEY", TEST_API_KEY)
	defer os.Unsetenv("INVYGO_API_KEY")

	// Keep it non-fatal if inventory can't be fetched (network / sheet issues).
	if _, err := getInventory(); err != nil {
		t.Logf("⚠️ inventory preload failed: %v", err)
	}

	tests := []struct {
		name    string
		payload string
		apiKey  string
	}{
		// Mirror the comprehensive suite inputs (no assertions here; we just record outputs).
		{"A1. Missing API Key", `{"city": "Riyadh"}`, ""},
		{"A2. Invalid API Key", `{"city": "Riyadh"}`, "wrong-key"},
		{"A3. Missing City (Mandatory)", `{"query": "Toyota"}`, TEST_API_KEY},
		{"A4. Empty JSON Payload", `{}`, TEST_API_KEY},

		{"B1. Subscribe to Own Only", `{"city": "Riyadh"}`, TEST_API_KEY},
		{"B2. Pricing Integrity (Fees > 0)", `{"city": "Riyadh"}`, TEST_API_KEY},

		{"C1. Brand Match (Toyota)", `{"city": "Riyadh", "query": "Toyota"}`, TEST_API_KEY},
		{"C2. Model Match (Yaris)", `{"city": "Riyadh", "query": "Yaris"}`, TEST_API_KEY},
		{"C3. Tier Match (Midsize Sedan)", `{"city": "Riyadh", "tier": "Midsize Sedan"}`, TEST_API_KEY},
		{"C4. Body Type Match (SUV)", `{"city": "Riyadh", "body_type": "SUV"}`, TEST_API_KEY},

		{"D1. Tier Fallback (Specific Car Missing -> Show Same Tier)", `{"city": "Riyadh", "query": "NonExistentCar", "tier": "Entry level Sedan"}`, TEST_API_KEY},
		{"D2. Body Type Fallback (Specific Car Missing -> Show Same Body)", `{"city": "Riyadh", "query": "Ferrari", "body_type": "SUV"}`, TEST_API_KEY},

		{"E1. Budget Match (No Specific Car)", `{"city": "Riyadh", "max_price": 2000}`, TEST_API_KEY},
		{"E2. Upsell (Budget Too Low)", `{"city": "Riyadh", "max_price": 100}`, TEST_API_KEY},

		{"F1. Case Insensitivity", `{"city": "rIyAdH", "query": "tOyOtA"}`, TEST_API_KEY},
		{"F2. Empty String Parameters", `{"city": "Riyadh", "query": "", "tier": "", "body_type": ""}`, TEST_API_KEY},
		{"F3. Specific City Separation (Jeddah)", `{"city": "Jeddah"}`, TEST_API_KEY},
		{"F4. Combined Constraints (SUV + Price)", `{"city": "Riyadh", "body_type": "SUV", "max_price": 3000}`, TEST_API_KEY},

		{"G1. Query SUV with Null Parameters", `{"city": "Riyadh", "tier": null, "color": null, "query": "SUV", "body_type": null, "condition": null, "max_price": null}`, TEST_API_KEY},
		{"G2. Tier Entry Level SUV with Null Parameters", `{"city": "Riyadh", "tier": "Entry level SUV", "color": null, "query": null, "body_type": null, "condition": null, "max_price": null}`, TEST_API_KEY},
		{"G3. All Null Parameters Except City", `{"city": "Riyadh", "tier": null, "color": null, "query": null, "body_type": null, "condition": null, "max_price": null}`, TEST_API_KEY},
		{"G4. Query Sedan with Null Others", `{"city": "Riyadh", "query": "Sedan", "tier": null, "body_type": null}`, TEST_API_KEY},
		{"G5. Tier Midsize SUV Search", `{"city": "Riyadh", "tier": "Midsize SUV", "query": null, "body_type": null, "max_price": null}`, TEST_API_KEY},

		{"H1. Luxury Tier Search", `{"city": "Riyadh", "tier": "luxury", "color": null, "query": "", "body_type": "", "condition": "", "max_price": null}`, TEST_API_KEY},
		{"H2. Crossover Body Type Search", `{"city": "Riyadh", "tier": "", "color": "", "query": "", "body_type": "crossover", "condition": "", "max_price": null}`, TEST_API_KEY},
		{"H3. USED Condition Filter", `{"city": "Riyadh", "tier": "", "color": null, "query": "", "body_type": "", "condition": "USED", "max_price": null}`, TEST_API_KEY},
		{"H4. Garbage Query (No Match Fallback)", `{"city": "Riyadh", "tier": "", "color": null, "query": "asdfsafsf", "body_type": "", "condition": "", "max_price": null}`, TEST_API_KEY},
		{"H5. Budget Too Low (Price 10)", `{"city": "Riyadh", "tier": "", "color": null, "query": "", "body_type": "", "condition": "", "max_price": 10}`, TEST_API_KEY},
	}

	records := make([]recordedResponse, 0, len(tests))

	for _, tc := range tests {
		req, err := http.NewRequest("POST", "/search", bytes.NewBufferString(tc.payload))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if tc.apiKey != "" {
			req.Header.Set("x-api-key", tc.apiKey)
		}

		rr := httptest.NewRecorder()
		SearchHandler(rr, req)

		rawBody := rr.Body.String()
		var parsed map[string]interface{}
		_ = json.Unmarshal(rr.Body.Bytes(), &parsed) // best-effort; keep RawBody always

		records = append(records, recordedResponse{
			Name:           tc.name,
			Payload:        json.RawMessage(tc.payload),
			APIKeyProvided: tc.apiKey != "",
			StatusCode:     rr.Code,
			Body:           parsed,
			RawBody:        rawBody,
			RecordedAt:     time.Now(),
		})
	}

	outDir := filepath.Join("testdata")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("failed to create %s: %v", outDir, err)
	}

	outPath := filepath.Join(outDir, "responses.json")
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal responses: %v", err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", outPath, err)
	}

	fmt.Printf("\nRecorded %d responses to %s\n", len(records), outPath)
}
