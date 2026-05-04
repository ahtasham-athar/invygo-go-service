package main

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ========== Types ==========

// AppointmentSlot represents a single appointment slot from the Dubai clinic sheet.
// Sheet column layout (A..K):
//   A country, B city, C clinic_name, D doctor_name, E speciality,
//   F location, G consultation_fee, H surgery_fee,
//   I slot_number, J slot_day, K slot_time
// Whether a slot is actually free is decided by the calendar tool, not this sheet.
type AppointmentSlot struct {
	SlotID          string  `json:"slot_id"`
	Country         string  `json:"country"`
	City            string  `json:"city"`
	ClinicName      string  `json:"clinic_name"`
	DoctorName      string  `json:"doctor_name"`
	Speciality      string  `json:"speciality"`
	Location        string  `json:"location"`
	ConsultationFee float64 `json:"consultation_fee"`
	SurgeryFee      float64 `json:"surgery_fee"`
	SlotNumber      int     `json:"slot_number"`
	SlotDay         string  `json:"slot_day"`
	SlotTime        string  `json:"slot_time"`
}

// HealthcareSearchRequest is what the LLM tool sends in.
type HealthcareSearchRequest struct {
	Country       string  `json:"country"`
	City          string  `json:"city"`
	ClinicName    string  `json:"clinic_name"`
	DoctorName    string  `json:"doctor_name"`
	Speciality    string  `json:"speciality"`
	Location      string  `json:"location"`
	SlotDay       string  `json:"slot_day"`
	SlotTime      string  `json:"slot_time"`
	MaxConsultFee float64 `json:"max_consult_fee"`
	MaxSurgeryFee float64 `json:"max_surgery_fee"`
}

// ========== Config ==========

const API_KEY = "f990ae1905ce649875800f3d3c39a05d42b2aa8b6d760303811a738e3f20zz92"

const HEALTHCARE_CSV_URL = "https://docs.google.com/spreadsheets/d/1JC58VJi0igde5MSBkRODPNxq_PdtIOlP5cTFVHh3_JA/export?format=csv"

const cacheTTL = 10 * time.Minute

// init registers the clinic-demo route on the default ServeMux. main.go's
// http.ListenAndServe(":8080", nil) picks it up automatically.
func init() {
	http.HandleFunc("/filter_healthcare", HandleFilterHealthcare)
}

// ========== Cache ==========

type appointmentCache struct {
	slots     []AppointmentSlot
	expiresAt time.Time
	mu        sync.RWMutex
}

var inventoryCache appointmentCache

// ========== Helpers ==========

// cleanHealthcarePrice: "1,999" or "AED 4120.00" -> 1999.0 / 4120.0
func cleanHealthcarePrice(price string) float64 {
	re := regexp.MustCompile(`[^\d.]`)
	cleaned := re.ReplaceAllString(price, "")
	result, _ := strconv.ParseFloat(cleaned, 64)
	return result
}

// generateSlotID builds a stable, deterministic ID for a (clinic, doctor, day, time, slot_number) row.
func generateSlotID(clinicName, doctorName, slotDay, slotTime string, slotNumber int) string {
	uniqueString := fmt.Sprintf("%s|%s|%s|%s|%d",
		strings.TrimSpace(strings.ToLower(clinicName)),
		strings.TrimSpace(strings.ToLower(doctorName)),
		strings.TrimSpace(strings.ToLower(slotDay)),
		strings.TrimSpace(strings.ToLower(slotTime)),
		slotNumber)
	hash := sha256.Sum256([]byte(uniqueString))
	hashStr := hex.EncodeToString(hash[:])[:12]
	return fmt.Sprintf("SLOT-%s", hashStr)
}

// ========== CSV Loading ==========

func loadAppointmentsCSVFromReader(r io.Reader) ([]AppointmentSlot, error) {
	reader := csv.NewReader(r)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	var slots []AppointmentSlot
	for i, row := range records {
		if i == 0 {
			continue
		}
		if len(row) < 11 {
			continue
		}

		consultationFee := cleanHealthcarePrice(strings.TrimSpace(row[6]))
		surgeryFee := cleanHealthcarePrice(strings.TrimSpace(row[7]))
		slotNumber, _ := strconv.Atoi(strings.TrimSpace(row[8]))

		clinicName := strings.TrimSpace(row[2])
		doctorName := strings.TrimSpace(row[3])
		slotDay := strings.TrimSpace(row[9])
		slotTime := strings.TrimSpace(row[10])

		slot := AppointmentSlot{
			SlotID:          generateSlotID(clinicName, doctorName, slotDay, slotTime, slotNumber),
			Country:         strings.TrimSpace(row[0]),
			City:            strings.TrimSpace(row[1]),
			ClinicName:      clinicName,
			DoctorName:      doctorName,
			Speciality:      strings.TrimSpace(row[4]),
			Location:        strings.TrimSpace(row[5]),
			ConsultationFee: consultationFee,
			SurgeryFee:      surgeryFee,
			SlotNumber:      slotNumber,
			SlotDay:         slotDay,
			SlotTime:        slotTime,
		}
		slots = append(slots, slot)
	}
	return slots, nil
}

func loadAppointmentsFromURL(url string) ([]AppointmentSlot, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch CSV: HTTP %d", resp.StatusCode)
	}
	return loadAppointmentsCSVFromReader(resp.Body)
}

// ========== Cache ==========

func GetInventory() ([]AppointmentSlot, error) {
	inventoryCache.mu.RLock()
	cached := inventoryCache.slots
	exp := inventoryCache.expiresAt
	inventoryCache.mu.RUnlock()

	if cached == nil || !time.Now().Before(exp) {
		slots, err := loadAppointmentsFromURL(HEALTHCARE_CSV_URL)
		if err != nil {
			log.Printf("Error loading appointments: %v", err)
			return nil, err
		}
		inventoryCache.mu.Lock()
		inventoryCache.slots = slots
		inventoryCache.expiresAt = time.Now().Add(cacheTTL)
		inventoryCache.mu.Unlock()
		cached = slots
	}

	return cached, nil
}

// ========== Filtering ==========

func FilterAppointments(req HealthcareSearchRequest, slots []AppointmentSlot) ([]AppointmentSlot, string, string) {
	country := strings.ToLower(strings.TrimSpace(req.Country))
	city := strings.ToLower(strings.TrimSpace(req.City))
	clinicName := strings.ToLower(strings.TrimSpace(req.ClinicName))
	doctorName := strings.ToLower(strings.TrimSpace(req.DoctorName))
	speciality := strings.ToLower(strings.TrimSpace(req.Speciality))
	location := strings.ToLower(strings.TrimSpace(req.Location))
	slotDay := strings.ToLower(strings.TrimSpace(req.SlotDay))
	slotTime := strings.ToLower(strings.TrimSpace(req.SlotTime))
	maxConsultFee := req.MaxConsultFee
	maxSurgeryFee := req.MaxSurgeryFee

	var filtered []AppointmentSlot

	for _, slot := range slots {
		if country != "" && strings.ToLower(strings.TrimSpace(slot.Country)) != country {
			continue
		}
		if city != "" && strings.ToLower(strings.TrimSpace(slot.City)) != city {
			continue
		}
		if clinicName != "" {
			if !strings.Contains(strings.ToLower(strings.TrimSpace(slot.ClinicName)), clinicName) {
				continue
			}
		}
		if doctorName != "" {
			if !strings.Contains(strings.ToLower(strings.TrimSpace(slot.DoctorName)), doctorName) {
				continue
			}
		}
		if speciality != "" && strings.ToLower(strings.TrimSpace(slot.Speciality)) != speciality {
			continue
		}
		if location != "" {
			if !strings.Contains(strings.ToLower(strings.TrimSpace(slot.Location)), location) {
				continue
			}
		}
		if slotDay != "" && strings.ToLower(strings.TrimSpace(slot.SlotDay)) != slotDay {
			continue
		}
		if slotTime != "" {
			if !strings.Contains(strings.ToLower(strings.TrimSpace(slot.SlotTime)), slotTime) {
				continue
			}
		}
		if maxConsultFee > 0 && slot.ConsultationFee > maxConsultFee {
			continue
		}
		if maxSurgeryFee > 0 && slot.SurgeryFee > maxSurgeryFee {
			continue
		}

		filtered = append(filtered, slot)
	}

	if len(filtered) == 0 {
		return nil, "No Appointments Found", "No appointment slots match your criteria."
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].ConsultationFee != filtered[j].ConsultationFee {
			return filtered[i].ConsultationFee < filtered[j].ConsultationFee
		}
		return filtered[i].SlotTime < filtered[j].SlotTime
	})

	return filtered, "Appointments Found", ""
}

// ========== HTTP Handlers ==========

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func HandleFilterHealthcare(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != API_KEY {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"error": "Unauthorized", "message": "A valid API key is required.", "status": 401,
		})
		return
	}

	var req HealthcareSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": "InvalidRequest", "message": "Invalid JSON payload", "status": 400,
		})
		return
	}

	slots, err := GetInventory()
	if err != nil {
		log.Printf("Inventory load failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error": "InventoryLoadError", "message": "Unable to load slots.", "status": 500, "details": err.Error(),
		})
		return
	}

	results, status, message := FilterAppointments(req, slots)

	resp := map[string]interface{}{
		"status":      status,
		"results":     results,
		"total_count": len(results),
	}
	if message != "" {
		resp["message"] = message
	}
	writeJSON(w, http.StatusOK, resp)
}

