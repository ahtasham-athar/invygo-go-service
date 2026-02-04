package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- Configuration ---
const API_KEY = "f990ae1905ce649875800f3d3c39a05d42b2aa8b6d760303811a738e3f20z999"

// UPDATED SHEET URL 
const CSV_URL = "https://docs.google.com/spreadsheets/d/1gds7nkXI6pPY_mCb1_55AoagcRU87ZnaWMYQN9YPDFA/export?format=csv"

const CACHE_TTL = 60 * time.Minute

// --- Data Models ---
type Car struct {
	Country    string  `json:"country"`
	City       string  `json:"city"`
	Brand      string  `json:"brand_name"`
	Model      string  `json:"car_name_fixed"`
	Year       int     `json:"manufacturing_year"`
	Trim       string  `json:"trim"`
	Color      string  `json:"color"`
	StarterFee float64 `json:"starter_fee"`
	MonthlyFee float64 `json:"monthly_fee"`
	Condition  string  `json:"car_condition"`
	Tier       string  `json:"tier"`       // Column K (Index 10)
	BodyType   string  `json:"body_type"`  // Column L (Index 11)

	// Enriched
	PlanType   string `json:"plan_type"`
	IsHighSpec bool   `json:"is_high_spec"`
}

type GroupedResult struct {
	Description     string   `json:"description"`
	Brand           string   `json:"brand"`
	Model           string   `json:"model"`
	Year            int      `json:"year"`
	Condition       string   `json:"condition"`
	BodyType        string   `json:"body_type"`
	Tier            string   `json:"tier"` // <--- CRITICAL: This must be here
	PlanType        string   `json:"plan_type"`
	MonthlyFee      float64  `json:"monthly_fee"`
	StarterFee      float64  `json:"starter_fee"`
	AvailableColors []string `json:"available_colors"`
	Features        string   `json:"features"`
}

type SearchRequest struct {
	City      string  `json:"city"`
	Query     string  `json:"query"`
	MaxPrice  float64 `json:"max_price"`
	Color     string  `json:"color"`
	Condition string  `json:"condition"`
	Tier      string  `json:"tier"`      // e.g. "Midsize Sedan"
	BodyType  string  `json:"body_type"` // e.g. "Sedan"
}

type InventoryCache struct {
	Cars      []Car
	ExpiresAt time.Time
	Mu        sync.RWMutex
}

var globalCache InventoryCache

// --- Helpers ---
func cleanPrice(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "\"", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
func cleanYear(s string) int {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "\"", "")
	i, _ := strconv.Atoi(s)
	return i
}

// --- Loader ---
func loadCarsFromURL(url string) ([]Car, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	reader := csv.NewReader(resp.Body)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	var loaded []Car
	for i, row := range records {
		// 0:country, 1:city, 2:brand, 3:model, 4:year, 5:trim, 6:color, 7:starter, 8:monthly, 9:condition, 10:Tier, 11:body_type
		if i == 0 || len(row) < 12 {
			continue
		}

		c := Car{
			Country:    row[0],
			City:       strings.TrimSpace(row[1]),
			Brand:      strings.TrimSpace(row[2]),
			Model:      strings.TrimSpace(row[3]),
			Year:       cleanYear(row[4]),
			Trim:       strings.TrimSpace(row[5]),
			Color:      strings.TrimSpace(row[6]),
			StarterFee: cleanPrice(row[7]),
			MonthlyFee: cleanPrice(row[8]),
			Condition:  strings.ToUpper(row[9]),
			Tier:       strings.TrimSpace(row[10]), 
			BodyType:   strings.TrimSpace(row[11]), 

			PlanType: "Subscribe to Own",
		}

		trimUpper := strings.ToUpper(c.Trim)
		if strings.Contains(trimUpper, "SMART") || strings.Contains(trimUpper, "DELUXE") || strings.Contains(trimUpper, "PREMIUM") || strings.Contains(trimUpper, "FULL") {
			c.IsHighSpec = true
		}
		loaded = append(loaded, c)
	}
	return loaded, nil
}

func getInventory() ([]Car, error) {
	globalCache.Mu.RLock()
	cached := globalCache.Cars
	expiry := globalCache.ExpiresAt
	globalCache.Mu.RUnlock()

	if len(cached) > 0 && time.Now().Before(expiry) {
		return cached, nil
	}

	freshCars, err := loadCarsFromURL(CSV_URL)
	if err != nil {
		if len(cached) > 0 {
			return cached, nil
		}
		return nil, err
	}
	globalCache.Mu.Lock()
	globalCache.Cars = freshCars
	globalCache.ExpiresAt = time.Now().Add(CACHE_TTL)
	globalCache.Mu.Unlock()
	return freshCars, nil
}

// --- FILTERING ---

func filterCars(req SearchRequest, inventory []Car) ([]GroupedResult, string, string) {
	reqCity := strings.ToLower(strings.TrimSpace(req.City))
	reqQuery := strings.ToLower(strings.TrimSpace(req.Query))
	reqCond := strings.ToUpper(strings.TrimSpace(req.Condition))
	reqBody := strings.ToLower(strings.TrimSpace(req.BodyType))
	reqTier := strings.ToLower(strings.TrimSpace(req.Tier))

	// --- PHASE 0: Base Filtering ---
	var baseSet []Car
	for _, car := range inventory {
		if strings.ToLower(car.City) != reqCity {
			continue
		}
		if reqCond != "" && car.Condition != reqCond {
			continue
		}
		baseSet = append(baseSet, car)
	}

	if len(baseSet) == 0 {
		return nil, "No Cars Found", "I'm sorry, we currently have no cars available in " + req.City + "."
	}

	// --- PHASE 1: Ideal Match ---
	var layer1 []Car
	for _, car := range baseSet {
		match := true

		// 1. Color Logic (Conditional)
		// Only filter by color if the user actually provided one.
		if req.Color != "" {
			if !strings.EqualFold(strings.TrimSpace(car.Color), strings.TrimSpace(req.Color)) {
				match = false
			}
		}

		// Strict Tier/Body Logic
		if reqTier != "" && strings.ToLower(car.Tier) != reqTier {
			match = false
		}
		if reqBody != "" && strings.ToLower(car.BodyType) != reqBody {
			match = false
		}
		// Query Logic
		if reqQuery != "" {
			// fullText := strings.ToLower(car.Brand + " " + car.Model)
			fullText := strings.ToLower(car.Brand + " " + car.Model + " " + car.BodyType)
			if !strings.Contains(fullText, reqQuery) {
				match = false
			}
		}
		// Price Logic
		if req.MaxPrice > 0 && car.MonthlyFee > req.MaxPrice {
			match = false
		}

		if match {
			layer1 = append(layer1, car)
		}
	}

	if len(layer1) > 0 {
		return groupResults(layer1), "Ideal Match Found", "Great news! I found exactly what you're looking for."
	}

	// --- PHASE 1.5: Smart Fallback ---
	var smartFallback []Car
	availableTiers := make(map[string]bool)
	targetBodyType := reqBody

	// Inference
	if targetBodyType == "" && reqTier != "" {
		if strings.Contains(reqTier, "sedan") {
			targetBodyType = "sedan"
		} else if strings.Contains(reqTier, "suv") {
			targetBodyType = "suv"
		}
	}

	if targetBodyType != "" {
		for _, car := range baseSet {
			if strings.ToLower(car.BodyType) == targetBodyType {
				smartFallback = append(smartFallback, car)
				availableTiers[car.Tier] = true
			}
		}
	}

	if len(smartFallback) > 0 {
		var tiersList []string
		minPrice := 99999.0
		for t := range availableTiers {
			tiersList = append(tiersList, t)
		}
		for _, car := range smartFallback {
			if car.MonthlyFee < minPrice {
				minPrice = car.MonthlyFee
			}
		}
		msg := fmt.Sprintf("The specific car isn't available, but we have these '%s' options in %s (%v) starting from %.0f SAR/month. Please ask the user to choose a Tier or check their budget.",
			targetBodyType, req.City, strings.Join(tiersList, ", "), minPrice)

		return groupResults(smartFallback), "Category Match Found", msg
	}

	// --- PHASE 2: Budget Match ---
	if req.MaxPrice > 0 {
		var layer2 []Car
		for _, car := range baseSet {
			if car.MonthlyFee <= req.MaxPrice {
				layer2 = append(layer2, car)
			}
		}
		sort.Slice(layer2, func(i, j int) bool { return layer2[i].MonthlyFee > layer2[j].MonthlyFee })

		if len(layer2) > 0 {
			return groupResults(layer2), "Budget Match Found", "I couldn't find that specific type, but I found these options within your budget."
		}
	}

	// --- PHASE 3: Upsell ---
	sort.Slice(baseSet, func(i, j int) bool { return baseSet[i].MonthlyFee < baseSet[j].MonthlyFee })
	upsell := baseSet
	if len(upsell) > 20 {
		upsell = upsell[:20]
	}

	msg := fmt.Sprintf("I don't have anything within your criteria, but here are our available options starting from %.0f/month.", upsell[0].MonthlyFee)
	return groupResults(upsell), "Upsell Options Found", msg
}

func groupResults(cars []Car) []GroupedResult {
	grouped := make(map[string]*GroupedResult)
	var order []string

	for _, car := range cars {
		key := fmt.Sprintf("%s-%s-%d-%s-%.0f", car.Brand, car.Model, car.Year, car.Condition, car.MonthlyFee)
		if _, exists := grouped[key]; !exists {
			feat := ""
			if car.IsHighSpec {
				feat += "High Spec "
			}

			grouped[key] = &GroupedResult{
				Description:     fmt.Sprintf("%s %s %d", car.Brand, car.Model, car.Year),
				Brand:           car.Brand,
				Model:           car.Model,
				Year:            car.Year,
				Condition:       car.Condition,
				BodyType:        car.BodyType,
				Tier:            car.Tier, // Ensure this is mapped!
				PlanType:        car.PlanType,
				MonthlyFee:      car.MonthlyFee,
				StarterFee:      car.StarterFee,
				AvailableColors: []string{},
				Features:        strings.TrimSpace(feat),
			}
			order = append(order, key)
		}
		isNew := true
		for _, c := range grouped[key].AvailableColors {
			if c == car.Color {
				isNew = false
				break
			}
		}
		if isNew {
			grouped[key].AvailableColors = append(grouped[key].AvailableColors, car.Color)
		}
	}

	var results []GroupedResult
	for _, key := range order {
		results = append(results, *grouped[key])
	}
	return results
}

// HANDLER
func SearchHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("x-api-key") != API_KEY {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "Unauthorized"})
		return
	}
	var req SearchRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.City == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "Error", "message": "City is mandatory."})
		return
	}
	cars, _ := getInventory()
	results, status, msg := filterCars(req, cars)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": status, "message": msg, "results": results,
	})
}

func main() {
	_, err := getInventory()
	if err != nil {
		log.Println("Init Error:", err)
	} else {
		log.Println("Inventory Loaded.")
	}
	http.HandleFunc("/search", SearchHandler)
	log.Println("Service running on :8889")
	log.Fatal(http.ListenAndServe(":8889", nil))
}

// package main

// import (
// 	"encoding/csv"
// 	"encoding/json"
// 	"fmt"
// 	"log"
// 	"net/http"
// 	"sort"
// 	"strconv"
// 	"strings"
// 	"sync"
// 	"time"
// )

// // --- Configuration ---
// const API_KEY = "f990ae1905ce649875800f3d3c39a05d42b2aa8b6d760303811a738e3f20z999"

// // YOUR LIVE SHEET URL
// const CSV_URL = "https://docs.google.com/spreadsheets/d/16akbI2qWPMuUwdd73h2YXHnaVgZDVuJ99z83hYCeZ1U/export?format=csv"

// const CACHE_TTL = 1 * time.Minute

// // --- Data Models ---
// type Car struct {
// 	Country      string  `json:"country"`
// 	City         string  `json:"city"`
// 	Brand        string  `json:"brand_name"`
// 	Model        string  `json:"car_name_fixed"`
// 	Year         int     `json:"manufacturing_year"`
// 	Trim         string  `json:"trim"`
// 	Color        string  `json:"color"`
// 	StarterFee   float64 `json:"starter_fee"`
// 	MonthlyFee   float64 `json:"monthly_fee"`
// 	Condition    string  `json:"car_condition"`
//     // Availability, Seats, BestFor REMOVED as per instruction
// 	// BodyType is now mapped from the correct column index
// 	BodyType     string  `json:"body_type"`
	
// 	// Enriched
// 	PlanType     string  `json:"plan_type"` 
// 	IsHighSpec   bool    `json:"is_high_spec"`
// }

// type GroupedResult struct {
// 	Description     string   `json:"description"`
// 	Brand           string   `json:"brand"`
// 	Model           string   `json:"model"`
// 	Year            int      `json:"year"`
// 	Condition       string   `json:"condition"`
// 	BodyType        string   `json:"body_type"`
// 	PlanType        string   `json:"plan_type"`
// 	MonthlyFee      float64  `json:"monthly_fee"`
// 	StarterFee      float64  `json:"starter_fee"`
// 	AvailableColors []string `json:"available_colors"`
// 	Features        string   `json:"features"`
// }

// type SearchRequest struct {
// 	City           string  `json:"city"`
// 	Query          string  `json:"query"`
// 	MaxPrice       float64 `json:"max_price"`
// 	Color          string  `json:"color"`
// 	Condition      string  `json:"condition"`
//     // Intention REMOVED as logic is now prompt-driven
// }

// type InventoryCache struct {
// 	Cars      []Car
// 	ExpiresAt time.Time
// 	Mu        sync.RWMutex
// }
// var globalCache InventoryCache

// // --- Helpers ---
// func cleanPrice(s string) float64 {
// 	s = strings.ReplaceAll(s, ",", "")
// 	s = strings.ReplaceAll(s, "\"", "")
// 	f, _ := strconv.ParseFloat(s, 64)
// 	return f
// }
// func cleanYear(s string) int {
// 	s = strings.ReplaceAll(s, ",", "")
// 	s = strings.ReplaceAll(s, "\"", "")
// 	i, _ := strconv.Atoi(s)
// 	return i
// }

// // --- Loader ---
// func loadCarsFromURL(url string) ([]Car, error) {
// 	resp, err := http.Get(url)
// 	if err != nil { return nil, err }
// 	defer resp.Body.Close()

// 	reader := csv.NewReader(resp.Body)
// 	reader.FieldsPerRecord = -1
// 	records, err := reader.ReadAll()
// 	if err != nil { return nil, err }

// 	var loaded []Car
// 	for i, row := range records {
//         // Adjust check based on your CSV structure (11 columns used)
// 		if i == 0 || len(row) < 11 { continue } 

// 		c := Car{
// 			Country:      row[0],
// 			City:         strings.TrimSpace(row[1]),
// 			Brand:        strings.TrimSpace(row[2]),
// 			Model:        strings.TrimSpace(row[3]),
// 			Year:         cleanYear(row[4]),
// 			Trim:         strings.TrimSpace(row[5]),
// 			Color:        strings.TrimSpace(row[6]),
// 			StarterFee:   cleanPrice(row[7]),
// 			MonthlyFee:   cleanPrice(row[8]),
// 			Condition:    strings.ToUpper(row[9]),
//             // Column 10 is BodyType in your PDF/CSV snippet
// 			BodyType:     strings.TrimSpace(row[10]),
            
//             PlanType:     "Subscribe to Own", 
// 		}

// 		trimUpper := strings.ToUpper(c.Trim)
// 		if strings.Contains(trimUpper, "SMART") || strings.Contains(trimUpper, "DELUXE") || strings.Contains(trimUpper, "PREMIUM") || strings.Contains(trimUpper, "FULL") {
// 			c.IsHighSpec = true
// 		}
// 		loaded = append(loaded, c)
// 	}
// 	return loaded, nil
// }

// func getInventory() ([]Car, error) {
// 	globalCache.Mu.RLock()
// 	cached := globalCache.Cars
// 	expiry := globalCache.ExpiresAt
// 	globalCache.Mu.RUnlock()

// 	if len(cached) > 0 && time.Now().Before(expiry) { return cached, nil }

// 	freshCars, err := loadCarsFromURL(CSV_URL)
// 	if err != nil {
// 		if len(cached) > 0 { return cached, nil }
// 		return nil, err
// 	}
// 	globalCache.Mu.Lock()
// 	globalCache.Cars = freshCars
// 	globalCache.ExpiresAt = time.Now().Add(CACHE_TTL)
// 	globalCache.Mu.Unlock()
// 	return freshCars, nil
// }

// // --- FILTERING ---

// func filterCars(req SearchRequest, inventory []Car) ([]GroupedResult, string, string) {
// 	reqCity := strings.ToLower(strings.TrimSpace(req.City))
// 	reqQuery := strings.ToLower(strings.TrimSpace(req.Query))
// 	reqCond := strings.ToUpper(strings.TrimSpace(req.Condition))

// 	var baseSet []Car
// 	for _, car := range inventory {
// 		if strings.ToLower(car.City) != reqCity { continue }
// 		if reqCond != "" && car.Condition != reqCond { continue }
// 		baseSet = append(baseSet, car)
// 	}

// 	if len(baseSet) == 0 {
// 		return nil, "No Cars Found", "I'm sorry, we currently have no cars available in " + req.City + "."
// 	}

// 	// Layer 1: Ideal Match
// 	var layer1 []Car
// 	for _, car := range baseSet {
// 		if reqQuery != "" {
//             // Check Brand or Model
// 			fullText := strings.ToLower(car.Brand + " " + car.Model + " " + car.BodyType)
// 			if !strings.Contains(fullText, reqQuery) { continue }
// 		}
// 		if req.MaxPrice > 0 && car.MonthlyFee > req.MaxPrice { continue }
// 		layer1 = append(layer1, car)
// 	}
// 	if len(layer1) > 0 {
// 		return groupResults(layer1), "Ideal Match Found", "Great news! I found exactly what you're looking for."
// 	}

// 	// Layer 2: Budget Match
// 	if req.MaxPrice > 0 {
// 		var layer3 []Car
// 		for _, car := range baseSet {
// 			if car.MonthlyFee <= req.MaxPrice { layer3 = append(layer3, car) }
// 		}
// 		sort.Slice(layer3, func(i, j int) bool { return layer3[i].MonthlyFee > layer3[j].MonthlyFee })
// 		if len(layer3) > 0 {
// 			return groupResults(layer3), "Budget Match Found", "I found these options that fit perfectly within your monthly budget."
// 		}
// 	}

// 	// Layer 3: Upsell / Fallback
// 	sort.Slice(baseSet, func(i, j int) bool { return baseSet[i].MonthlyFee < baseSet[j].MonthlyFee })
// 	upsell := baseSet
// 	// Increase limit to 20 so the Agent can find "Similar Tier" cars that are more expensive than the cheapest ones
// 	if len(upsell) > 20 { upsell = upsell[:20] } 
// 	msg := fmt.Sprintf("I don't have anything within your budget of %.0f, but here are our available options.", req.MaxPrice)
// 	return groupResults(upsell), "Upsell Options Found", msg
	
// 	// Layer 3: Upsell
// 	// sort.Slice(baseSet, func(i, j int) bool { return baseSet[i].MonthlyFee < baseSet[j].MonthlyFee })
// 	// upsell := baseSet
// 	// if len(upsell) > 3 { upsell = upsell[:3] }
// 	// msg := fmt.Sprintf("I don't have anything within your budget of %.0f, but my options start from %.0f/month.", req.MaxPrice, upsell[0].MonthlyFee)
// 	// return groupResults(upsell), "Upsell Options Found", msg
// }

// func groupResults(cars []Car) []GroupedResult {
// 	grouped := make(map[string]*GroupedResult)
// 	var order []string

// 	for _, car := range cars {
// 		key := fmt.Sprintf("%s-%s-%d-%s-%.0f", car.Brand, car.Model, car.Year, car.Condition, car.MonthlyFee)
// 		if _, exists := grouped[key]; !exists {
// 			feat := ""
// 			if car.IsHighSpec { feat += "High Spec " }

// 			grouped[key] = &GroupedResult{
// 				Description:     fmt.Sprintf("%s %s %d", car.Brand, car.Model, car.Year),
// 				Brand:           car.Brand,
// 				Model:           car.Model,
// 				Year:            car.Year,
// 				Condition:       car.Condition,
// 				BodyType:        car.BodyType,
// 				PlanType:        car.PlanType,
// 				MonthlyFee:      car.MonthlyFee,
// 				StarterFee:      car.StarterFee,
// 				AvailableColors: []string{},
// 				Features:        strings.TrimSpace(feat),
// 			}
// 			order = append(order, key)
// 		}
// 		isNew := true
// 		for _, c := range grouped[key].AvailableColors {
// 			if c == car.Color { isNew = false; break }
// 		}
// 		if isNew { grouped[key].AvailableColors = append(grouped[key].AvailableColors, car.Color) }
// 	}

// 	var results []GroupedResult
// 	for _, key := range order { results = append(results, *grouped[key]) }
// 	// if len(results) > 3 { results = results[:3] }
// 	return results
// }

// // HANDLER
// func SearchHandler(w http.ResponseWriter, r *http.Request) {
// 	w.Header().Set("Content-Type", "application/json")
// 	if r.Header.Get("x-api-key") != API_KEY {
// 		w.WriteHeader(http.StatusUnauthorized)
// 		json.NewEncoder(w).Encode(map[string]interface{}{"error": "Unauthorized"})
// 		return
// 	}
// 	var req SearchRequest
// 	json.NewDecoder(r.Body).Decode(&req)
// 	if req.City == "" {
// 		json.NewEncoder(w).Encode(map[string]interface{}{"status": "Error", "message": "City is mandatory."})
// 		return
// 	}
// 	cars, _ := getInventory()
// 	results, status, msg := filterCars(req, cars)
// 	json.NewEncoder(w).Encode(map[string]interface{}{
// 		"status": status, "message": msg, "results": results,
// 	})
// }

// func main() {
// 	_, err := getInventory()
// 	if err != nil { log.Println("Init Error:", err) } else { log.Println("Inventory Loaded.") }
// 	http.HandleFunc("/search", SearchHandler)
// 	log.Println("Service running on :8080")
// 	log.Fatal(http.ListenAndServe(":8080", nil))
// }
