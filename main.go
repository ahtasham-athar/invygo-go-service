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

// REPLACE WITH YOUR ACTUAL SHEET URL
const CSV_URL = "https://docs.google.com/spreadsheets/d/16akbI2qWPMuUwdd73h2YXHnaVgZDVuJ99z83hYCeZ1U/export?format=csv"

const CACHE_TTL = 1 * time.Minute

// --- Data Models ---
type Car struct {
	Country      string  `json:"country"`
	City         string  `json:"city"`
	Brand        string  `json:"brand_name"`
	Model        string  `json:"car_name_fixed"`
	Year         int     `json:"manufacturing_year"`
	Trim         string  `json:"trim"`
	Color        string  `json:"color"`
	StarterFee   float64 `json:"starter_fee"`
	MonthlyFee   float64 `json:"monthly_fee"`
	Condition    string  `json:"car_condition"`
	Availability string  `json:"availability"`
	Seats        string  `json:"seating_capacity"`
	BestFor      string  `json:"best_for"`
	BodyType     string  `json:"body_type"`
	
	// Enriched
	PlanType     string  `json:"plan_type"` 
	IsHighSpec   bool    `json:"is_high_spec"`
}

type GroupedResult struct {
	Description     string   `json:"description"`
	Brand           string   `json:"brand"`
	Model           string   `json:"model"`
	Year            int      `json:"year"`
	Condition       string   `json:"condition"`
	BodyType        string   `json:"body_type"`
	PlanType        string   `json:"plan_type"`
	MonthlyFee      float64  `json:"monthly_fee"`
	StarterFee      float64  `json:"starter_fee"`
	AvailableColors []string `json:"available_colors"`
	Features        string   `json:"features"`
	BestFor         string   `json:"best_for"`
}

type SearchRequest struct {
	City           string  `json:"city"`
	Query          string  `json:"query"`
	Intention      string  `json:"intention"`
	MaxPrice       float64 `json:"max_price"`
	Color          string  `json:"color"`
	Condition      string  `json:"condition"`
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
	if err != nil { return nil, err }
	defer resp.Body.Close()

	reader := csv.NewReader(resp.Body)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil { return nil, err }

	var loaded []Car
	for i, row := range records {
		if i == 0 || len(row) < 14 { continue }

		c := Car{
			Country:      row[0],
			City:         strings.TrimSpace(row[1]),
			Brand:        strings.TrimSpace(row[2]),
			Model:        strings.TrimSpace(row[3]),
			Year:         cleanYear(row[4]),
			Trim:         strings.TrimSpace(row[5]),
			Color:        strings.TrimSpace(row[6]),
			StarterFee:   cleanPrice(row[7]),
			MonthlyFee:   cleanPrice(row[8]),
			Condition:    strings.ToUpper(row[9]),
			Availability: strings.TrimSpace(row[10]),
			Seats:        strings.TrimSpace(row[11]),
			BestFor:      strings.TrimSpace(row[12]),
			BodyType:     strings.TrimSpace(row[13]),
            
            // CRITICAL: Force all inventory to STO Logic
            PlanType:     "Subscribe to Own", 
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

	if len(cached) > 0 && time.Now().Before(expiry) { return cached, nil }

	freshCars, err := loadCarsFromURL(CSV_URL)
	if err != nil {
		if len(cached) > 0 { return cached, nil }
		return nil, err
	}
	globalCache.Mu.Lock()
	globalCache.Cars = freshCars
	globalCache.ExpiresAt = time.Now().Add(CACHE_TTL)
	globalCache.Mu.Unlock()
	return freshCars, nil
}

// --- FILTERING ---
func matchIntention(car Car, intention string) bool {
	if intention == "" || intention == "none" { return true }
	bestFor := strings.ToLower(car.BestFor)
	body := strings.ToLower(car.BodyType)
	switch intention {
	case "family": return strings.Contains(car.Seats, "7") || strings.Contains(bestFor, "family")
	case "desert": return strings.Contains(body, "suv") || strings.Contains(bestFor, "adventure")
	case "budget": return strings.Contains(bestFor, "budget") || strings.Contains(bestFor, "city")
	case "luxury": return car.IsHighSpec || car.MonthlyFee > 3000
	case "travel": return strings.Contains(bestFor, "comfort") || strings.Contains(bestFor, "travel")
	}
	return true
}

func filterCars(req SearchRequest, inventory []Car) ([]GroupedResult, string, string) {
	reqCity := strings.ToLower(strings.TrimSpace(req.City))
	reqQuery := strings.ToLower(strings.TrimSpace(req.Query))
	reqCond := strings.ToUpper(strings.TrimSpace(req.Condition))

	var baseSet []Car
	for _, car := range inventory {
		if strings.ToLower(car.Availability) != "available" { continue }
		if strings.ToLower(car.City) != reqCity { continue }
		if reqCond != "" && car.Condition != reqCond { continue }
		// Removed "PlanPreference" logic. All cars are valid.
		baseSet = append(baseSet, car)
	}

	if len(baseSet) == 0 {
		return nil, "No Cars Found", "I'm sorry, we currently have no cars available in " + req.City + "."
	}

	// Layer 1: Ideal Match
	var layer1 []Car
	for _, car := range baseSet {
		if reqQuery != "" {
			fullText := strings.ToLower(car.Brand + " " + car.Model + " " + car.BodyType)
			if !strings.Contains(fullText, reqQuery) { continue }
		}
		if req.MaxPrice > 0 && car.MonthlyFee > req.MaxPrice { continue }
		if !matchIntention(car, req.Intention) { continue }
		layer1 = append(layer1, car)
	}
	if len(layer1) > 0 {
		return groupResults(layer1), "Ideal Match Found", "Great news! I found exactly what you're looking for."
	}

	// Layer 2: Smart Swap
	if reqQuery != "" || req.Intention != "" {
		var layer2 []Car
		for _, car := range baseSet {
			if req.MaxPrice > 0 && car.MonthlyFee > req.MaxPrice { continue }
			if !matchIntention(car, req.Intention) { continue }
			layer2 = append(layer2, car)
		}
		if len(layer2) > 0 {
			return groupResults(layer2), "Alternative Suggestions Found", "I couldn't find that specific model, but I found these excellent alternatives."
		}
	}

	// Layer 3: Budget Match
	if req.MaxPrice > 0 {
		var layer3 []Car
		for _, car := range baseSet {
			if car.MonthlyFee <= req.MaxPrice { layer3 = append(layer3, car) }
		}
		sort.Slice(layer3, func(i, j int) bool { return layer3[i].MonthlyFee > layer3[j].MonthlyFee })
		if len(layer3) > 0 {
			return groupResults(layer3), "Budget Match Found", "I found these options that fit perfectly within your monthly budget."
		}
	}

	// Layer 4: Upsell
	sort.Slice(baseSet, func(i, j int) bool { return baseSet[i].MonthlyFee < baseSet[j].MonthlyFee })
	upsell := baseSet
	if len(upsell) > 3 { upsell = upsell[:3] }
	msg := fmt.Sprintf("I don't have anything within your budget of %.0f, but my options start from %.0f/month.", req.MaxPrice, upsell[0].MonthlyFee)
	return groupResults(upsell), "Upsell Options Found", msg
}

func groupResults(cars []Car) []GroupedResult {
	grouped := make(map[string]*GroupedResult)
	var order []string

	for _, car := range cars {
		key := fmt.Sprintf("%s-%s-%d-%s-%.0f", car.Brand, car.Model, car.Year, car.Condition, car.MonthlyFee)
		if _, exists := grouped[key]; !exists {
			feat := ""
			if car.IsHighSpec { feat += "High Spec " }
			if strings.Contains(car.Seats, "7") { feat += "7-Seater " }

			grouped[key] = &GroupedResult{
				Description:     fmt.Sprintf("%s %s %d", car.Brand, car.Model, car.Year),
				Brand:           car.Brand,
				Model:           car.Model,
				Year:            car.Year,
				Condition:       car.Condition,
				BodyType:        car.BodyType,
				PlanType:        car.PlanType,
				MonthlyFee:      car.MonthlyFee,
				StarterFee:      car.StarterFee,
				AvailableColors: []string{},
				Features:        strings.TrimSpace(feat),
				BestFor:         car.BestFor,
			}
			order = append(order, key)
		}
		isNew := true
		for _, c := range grouped[key].AvailableColors {
			if c == car.Color { isNew = false; break }
		}
		if isNew { grouped[key].AvailableColors = append(grouped[key].AvailableColors, car.Color) }
	}

	var results []GroupedResult
	for _, key := range order { results = append(results, *grouped[key]) }
	if len(results) > 3 { results = results[:3] }
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
	if err != nil { log.Println("Init Error:", err) } else { log.Println("Inventory Loaded.") }
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


// const API_KEY = "f990ae1905ce649875800f3d3c39a05d42b2aa8b6d760303811a738e3f20z999"

// // 2. Data Source
// // CRITICAL: Replace 'YOUR_NEW_SHEET_ID_HERE' with the ID of your new "Invygo Inventory List"
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
// 	Availability string  `json:"availability"`
// 	Seats        string  `json:"seating_capacity"`
// 	BestFor      string  `json:"best_for"`
// 	BodyType     string  `json:"body_type"`

// 	PlanType   string `json:"plan_type"`
// 	IsHighSpec bool   `json:"is_high_spec"`
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
// 	BestFor         string   `json:"best_for"`
// }

// type SearchRequest struct {
// 	City           string  `json:"city"`
// 	PlanPreference string  `json:"plan_preference"`
// 	Query          string  `json:"query"`
// 	Intention      string  `json:"intention"`
// 	MaxPrice       float64 `json:"max_price"`
// 	Color          string  `json:"color"`
// 	Condition      string  `json:"condition"`
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
// 		if i == 0 || len(row) < 14 { continue }

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
// 			Availability: strings.TrimSpace(row[10]),
// 			Seats:        strings.TrimSpace(row[11]),
// 			BestFor:      strings.TrimSpace(row[12]),
// 			BodyType:     strings.TrimSpace(row[13]),
// 		}

// 		if c.StarterFee > 100 { c.PlanType = "Subscribe to Own" } else { c.PlanType = "Monthly Flex" }
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
// func matchIntention(car Car, intention string) bool {
// 	if intention == "" || intention == "none" { return true }
// 	bestFor := strings.ToLower(car.BestFor)
// 	body := strings.ToLower(car.BodyType)
// 	switch intention {
// 	case "family": return strings.Contains(car.Seats, "7") || strings.Contains(bestFor, "family")
// 	case "desert": return strings.Contains(body, "suv") || strings.Contains(bestFor, "adventure")
// 	case "budget": return strings.Contains(bestFor, "budget") || strings.Contains(bestFor, "city")
// 	case "luxury": return car.IsHighSpec || car.MonthlyFee > 3000
// 	case "travel": return strings.Contains(bestFor, "comfort") || strings.Contains(bestFor, "travel")
// 	}
// 	return true
// }

// func filterCars(req SearchRequest, inventory []Car) ([]GroupedResult, string, string) {
// 	reqCity := strings.ToLower(strings.TrimSpace(req.City))
// 	reqQuery := strings.ToLower(strings.TrimSpace(req.Query))
// 	reqCond := strings.ToUpper(strings.TrimSpace(req.Condition))

// 	var baseSet []Car
// 	for _, car := range inventory {
// 		if strings.ToLower(car.Availability) != "available" { continue }
// 		if strings.ToLower(car.City) != reqCity { continue }
// 		if reqCond != "" && car.Condition != reqCond { continue }
// 		if req.PlanPreference == "own" && car.PlanType != "Subscribe to Own" { continue }
// 		if req.PlanPreference == "rent" && car.PlanType != "Monthly Flex" { continue }
// 		baseSet = append(baseSet, car)
// 	}

// 	if len(baseSet) == 0 {
// 		return nil, "No Cars Found", "I'm sorry, we currently have no cars available in " + req.City + " for that plan type."
// 	}

// 	// Layer 1: Ideal Match
// 	var layer1 []Car
// 	for _, car := range baseSet {
// 		if reqQuery != "" {
// 			fullText := strings.ToLower(car.Brand + " " + car.Model + " " + car.BodyType)
// 			if !strings.Contains(fullText, reqQuery) { continue }
// 		}
// 		if req.MaxPrice > 0 && car.MonthlyFee > req.MaxPrice { continue }
// 		if !matchIntention(car, req.Intention) { continue }
// 		layer1 = append(layer1, car)
// 	}
// 	if len(layer1) > 0 {
// 		return groupResults(layer1), "Ideal Match Found", "Great news! I found exactly what you're looking for."
// 	}

// 	// Layer 2: Smart Swap (Intention Match)
// 	if reqQuery != "" || req.Intention != "" {
// 		var layer2 []Car
// 		for _, car := range baseSet {
// 			if req.MaxPrice > 0 && car.MonthlyFee > req.MaxPrice { continue }
// 			if !matchIntention(car, req.Intention) { continue }
// 			layer2 = append(layer2, car)
// 		}
// 		if len(layer2) > 0 {
// 			return groupResults(layer2), "Alternative Suggestions Found", "I couldn't find that specific model, but I found these cars that match your needs perfectly."
// 		}
// 	}

// 	// Layer 3: Budget Match
// 	if req.MaxPrice > 0 {
// 		var layer3 []Car
// 		for _, car := range baseSet {
// 			if car.MonthlyFee <= req.MaxPrice { layer3 = append(layer3, car) }
// 		}
// 		sort.Slice(layer3, func(i, j int) bool { return layer3[i].MonthlyFee > layer3[j].MonthlyFee })
// 		if len(layer3) > 0 {
// 			return groupResults(layer3), "Budget Match Found", "I couldn't find a car with those exact specs, but here are the best options within your budget."
// 		}
// 	}

// 	// Layer 4: Upsell
// 	sort.Slice(baseSet, func(i, j int) bool { return baseSet[i].MonthlyFee < baseSet[j].MonthlyFee })
// 	upsell := baseSet
// 	if len(upsell) > 3 { upsell = upsell[:3] }
// 	msg := fmt.Sprintf("I don't have anything within your budget of %.0f, but my options start from %.0f.", req.MaxPrice, upsell[0].MonthlyFee)
// 	return groupResults(upsell), "Upsell Options Found", msg
// }

// func groupResults(cars []Car) []GroupedResult {
// 	grouped := make(map[string]*GroupedResult)
// 	var order []string

// 	for _, car := range cars {
// 		key := fmt.Sprintf("%s-%s-%d-%s-%.0f-%s", car.Brand, car.Model, car.Year, car.Condition, car.MonthlyFee, car.PlanType)
// 		if _, exists := grouped[key]; !exists {
// 			feat := ""
// 			if car.IsHighSpec { feat += "High Spec " }
// 			if strings.Contains(car.Seats, "7") { feat += "7-Seater " }

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
// 				BestFor:         car.BestFor,
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
// 	if len(results) > 3 { results = results[:3] }
// 	return results
// }

// func main() {
// 	_, err := getInventory()
// 	if err != nil { log.Println("Init Error:", err) } else { log.Println("Inventory Loaded.") }

// 	http.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
// 		w.Header().Set("Content-Type", "application/json") // FORCE JSON
		
// 		if r.Header.Get("x-api-key") != API_KEY {
// 			w.WriteHeader(http.StatusUnauthorized)
// 			json.NewEncoder(w).Encode(map[string]interface{}{"error": "Unauthorized"})
// 			return
// 		}

// 		var req SearchRequest
// 		json.NewDecoder(r.Body).Decode(&req)
		
// 		if req.City == "" {
// 			json.NewEncoder(w).Encode(map[string]interface{}{"status": "Error", "message": "City is mandatory."})
// 			return
// 		}

// 		cars, _ := getInventory()
// 		results, status, msg := filterCars(req, cars)

// 		json.NewEncoder(w).Encode(map[string]interface{}{
// 			"status": status, "message": msg, "results": results,
// 		})
// 	})
// 	log.Fatal(http.ListenAndServe(":8080", nil))
// }
