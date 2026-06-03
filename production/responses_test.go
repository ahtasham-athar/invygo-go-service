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

// TestRecordAllResponses snapshots real outputs for the prompt-engineering phase.
func TestRecordAllResponses(t *testing.T) {
	if _, _, err := getInventory(); err != nil {
		t.Logf("⚠️ inventory preload failed: %v", err)
	}

	tests := []struct {
		name    string
		payload string
		apiKey  string
	}{
		{"A1. Missing API Key", `{"city":"Riyadh"}`, ""},
		{"A2. Invalid API Key", `{"city":"Riyadh"}`, "wrong-key"},
		{"A3. Missing City", `{"query":"Toyota"}`, TEST_API_KEY},

		{"STO. Brand ideal", `{"city":"Riyadh","product":"STO","query":"Toyota"}`, TEST_API_KEY},
		{"STO. Model ideal", `{"city":"Riyadh","product":"STO","query":"Yaris"}`, TEST_API_KEY},
		{"STO. Body+budget", `{"city":"Riyadh","product":"STO","body_type":"SUV","max_price":3000}`, TEST_API_KEY},
		{"STO. Fallback unknown model + body", `{"city":"Riyadh","product":"STO","query":"Ferrari","body_type":"Sedan","max_price":2500}`, TEST_API_KEY},
		{"STO. Need body type", `{"city":"Riyadh","product":"STO","query":"Zxqwerty"}`, TEST_API_KEY},
		{"STO. Budget needed (no budget)", `{"city":"Riyadh","product":"STO","body_type":"Sedan"}`, TEST_API_KEY},
		{"STO. Above budget only", `{"city":"Riyadh","product":"STO","body_type":"SUV","max_price":300}`, TEST_API_KEY},
		{"STO. Luxury tier direct", `{"city":"Riyadh","product":"STO","tier":"Luxury"}`, TEST_API_KEY},

		{"MONTHLY. Broad", `{"city":"Jeddah","product":"MONTHLY"}`, TEST_API_KEY},
		{"MONTHLY. Sedan + budget", `{"city":"Jeddah","product":"MONTHLY","body_type":"Sedan","max_price":2500}`, TEST_API_KEY},

		{"STS. Broad weekly", `{"city":"Riyadh","product":"STS"}`, TEST_API_KEY},
		{"STS. SUV", `{"city":"Riyadh","product":"STS","body_type":"SUV"}`, TEST_API_KEY},

		{"City. Dammam", `{"city":"Dammam"}`, TEST_API_KEY},
		{"City. Madinah", `{"city":"Madinah"}`, TEST_API_KEY},
		{"City. Unknown", `{"city":"Atlantis"}`, TEST_API_KEY},
	}

	records := make([]recordedResponse, 0, len(tests))
	for _, tc := range tests {
		req, _ := http.NewRequest("POST", "/search", bytes.NewBufferString(tc.payload))
		req.Header.Set("Content-Type", "application/json")
		if tc.apiKey != "" {
			req.Header.Set("x-api-key", tc.apiKey)
		}
		rr := httptest.NewRecorder()
		SearchHandler(rr, req)

		var parsed map[string]interface{}
		_ = json.Unmarshal(rr.Body.Bytes(), &parsed)
		records = append(records, recordedResponse{
			Name:           tc.name,
			Payload:        json.RawMessage(tc.payload),
			APIKeyProvided: tc.apiKey != "",
			StatusCode:     rr.Code,
			Body:           parsed,
			RawBody:        rr.Body.String(),
			RecordedAt:     time.Time{},
		})
	}

	outDir := "testdata"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	b, _ := json.MarshalIndent(records, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "responses.json"), b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fmt.Printf("\nRecorded %d responses\n", len(records))
}
