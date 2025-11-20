package main

import (
	"encoding/csv"
	"encoding/json"
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

const API_KEY = "f990ae1905ce649875800f3d3c39a05d42b2aa8b6d760303811a738e3f20zz90"

// Use your Google Sheets CSV export URL (NOT the /edit link)
const CSV_URL = "https://docs.google.com/spreadsheets/d/1cTC1lQRibMRmcVi4bT6nxHLPE5OjuypNZ71XoY8_v0g/export?format=csv"

// Cache settings
const cacheTTL = time.Minute // 1 minute cache

type Car struct {
	Position         int    `json:"Position"`
	SubscriptionType string `json:"Subscription Type"`
	Availability     string `json:"Availability"`
	Year             int    `json:"Year"`
	CarModel         string `json:"Car Model"`
	ContractDuration string `json:"Contract Duration"`
	Mileage          string `json:"Mileage"`
	Fee              string `json:"Fee"`
	BodyType         string `json:"Body Type"`
}

type SearchRequest struct {
	CarModel string `json:"car_model"`
	BodyType string `json:"body_type"`
	MaxPrice string `json:"max_price"`
	MinYear  string `json:"min_year"`
}

type carCache struct {
	cars      []Car
	expiresAt time.Time
	mu        sync.RWMutex
}

var inventoryCache carCache

// cleanFee: "AED 4120.00" -> 4120.0
func cleanFee(fee string) float64 {
	re := regexp.MustCompile(`[^\d.]`)
	cleaned := re.ReplaceAllString(fee, "")
	price, _ := strconv.ParseFloat(cleaned, 64)
	return price
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---- CSV loading helpers ----

func loadCarsCSVFromReader(r io.Reader) ([]Car, error) {
	reader := csv.NewReader(r)
	// Optional: be tolerant to odd quoting
	reader.LazyQuotes = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	var cars []Car
	for i, row := range records {
		if i == 0 {
			continue // skip header
		}
		if len(row) < 9 {
			continue // guard against short rows
		}
		year, _ := strconv.Atoi(row[3])
		position, _ := strconv.Atoi(row[0])
		cars = append(cars, Car{
			Position:         position,
			SubscriptionType: row[1],
			Availability:     row[2],
			Year:            year,
			CarModel:        row[4],
			ContractDuration: row[5],
			Mileage:        row[6],
			Fee:            row[7],
			BodyType:      row[8],
		})
	}
	return cars, nil
}

func loadCarsFromURL(url string) ([]Car, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return loadCarsCSVFromReader(resp.Body)
}

// getInventory returns cached cars if fresh, otherwise reloads from CSV_URL
func getInventory() ([]Car, error) {
	inventoryCache.mu.RLock()
	cached := inventoryCache.cars
	exp := inventoryCache.expiresAt
	inventoryCache.mu.RUnlock()

	now := time.Now()
	if cached != nil && now.Before(exp) {
		return cached, nil
	}

	// Need to refresh cache
	cars, err := loadCarsFromURL(CSV_URL)
	if err != nil {
		return nil, err
	}

	inventoryCache.mu.Lock()
	inventoryCache.cars = cars
	inventoryCache.expiresAt = time.Now().Add(cacheTTL)
	inventoryCache.mu.Unlock()

	return cars, nil
}

// ---- Smart Search v2.1 filtering logic ----

// filterCars implements the exact same multi-layer logic as your JS code.
// Returns: matching cars, status string, and optional message ("" if none).
func filterCars(req SearchRequest, cars []Car) ([]Car, string, string) {
	// Step 4: clean user inputs
	minYear, _ := strconv.Atoi(req.MinYear)
	maxPrice := cleanFee(req.MaxPrice)
	bodyType := strings.ToLower(strings.TrimSpace(req.BodyType))

	// THE FIX: remove all spaces from user's car model input
	carModelInput := strings.ToLower(strings.TrimSpace(req.CarModel))
	carModelInput = strings.ReplaceAll(carModelInput, " ", "")

	// --- Step 3: master list of ONLY available cars ---
	available := []Car{}
	for _, c := range cars {
		if strings.ToLower(strings.TrimSpace(c.Availability)) == "available now" {
			available = append(available, c)
		}
	}

	// --- Layer 1: Ideal Match ---
	idealResults := []Car{}
	for _, car := range available {
		carYear := car.Year
		carPrice := cleanFee(car.Fee)
		carBodyType := strings.ToLower(strings.TrimSpace(car.BodyType))

		// THE FIX: remove all spaces from DB car model before comparing
		carModelFromDB := strings.ToLower(car.CarModel)
		carModelFromDB = strings.ReplaceAll(carModelFromDB, " ", "")

		// Only check criteria if the user actually provided them
		modelMatch := (carModelInput == "") || strings.Contains(carModelFromDB, carModelInput)
		bodyTypeMatch := (bodyType == "") || carBodyType == bodyType
		priceMatch := (maxPrice <= 0) || carPrice <= maxPrice
		yearMatch := (minYear <= 1900) || carYear >= minYear

		if modelMatch && bodyTypeMatch && priceMatch && yearMatch {
			idealResults = append(idealResults, car)
		}
	}

	if len(idealResults) > 0 {
		return idealResults[:min(3, len(idealResults))], "Ideal Match Found", ""
	}

	// --- Layer 2: Flexible Model + Budget Match ---
	if carModelInput != "" {
		type modelMatchWithDiff struct {
			Car       Car
			PriceDiff float64
		}
		mm := []modelMatchWithDiff{}

		for _, car := range available {
			dbCarModel := strings.ToLower(car.CarModel)
			dbCarModel = strings.ReplaceAll(dbCarModel, " ", "")
			if strings.Contains(dbCarModel, carModelInput) {
				price := cleanFee(car.Fee)
				diff := maxPrice - price
				if diff < 0 {
					diff = -diff
				}
				mm = append(mm, modelMatchWithDiff{
					Car:       car,
					PriceDiff: diff,
				})
			}
		}

		if len(mm) > 0 {
			// sort ascending by price_diff
			sort.Slice(mm, func(i, j int) bool {
				return mm[i].PriceDiff < mm[j].PriceDiff
			})

			// take top 3 cars
			out := []Car{}
			for i := 0; i < min(3, len(mm)); i++ {
				out = append(out, mm[i].Car)
			}

			msg := "I couldn't find a perfect match, but I found some other " +
				req.CarModel +
				" models that are very close to your budget."

			return out, "Flexible Model Match Found", msg
		}
	}

	// --- Layer 3: Alternative Suggestions (Relaxed Match) ---
	relaxedResults := []Car{}
	for _, car := range available {
		carYear := car.Year
		carPrice := cleanFee(car.Fee)
		carBodyType := strings.ToLower(strings.TrimSpace(car.BodyType))

		bodyTypeMatch := (bodyType == "") || carBodyType == bodyType
		priceMatch := (maxPrice <= 0) || carPrice <= maxPrice
		yearMatch := (minYear <= 1900) || carYear >= minYear

		if bodyTypeMatch && priceMatch && yearMatch {
			relaxedResults = append(relaxedResults, car)
		}
	}

	if len(relaxedResults) > 0 {
		yearPart := "any year"
		if strings.TrimSpace(req.MinYear) != "" {
			yearPart = req.MinYear
		}

		msg := "While I couldn't find that exact model, I did find some other great " +
			req.BodyType + "s from " + yearPart + " within your budget."

		return relaxedResults[:min(3, len(relaxedResults))], "Alternative Suggestions Found", msg
	}

	// --- Layer 4: Budget-First Match ---
	if maxPrice > 0 {
		type budgetMatch struct {
			Car          Car
			PriceNumeric float64
		}
		bm := []budgetMatch{}

		for _, car := range available {
			price := cleanFee(car.Fee)
			if price <= maxPrice {
				bm = append(bm, budgetMatch{
					Car:          car,
					PriceNumeric: price,
				})
			}
		}

		if len(bm) > 0 {
			// sort descending by price_numeric (most premium first)
			sort.Slice(bm, func(i, j int) bool {
				return bm[i].PriceNumeric > bm[j].PriceNumeric
			})

			out := []Car{}
			for i := 0; i < min(3, len(bm)); i++ {
				out = append(out, bm[i].Car)
			}

			msg := "It seems we don't have cars matching all your criteria right now. " +
				"However, based on your budget, here are a few excellent options I can offer."

			return out, "Budget Match Found", msg
		}
	}

	// --- Final Layer: No Cars Found ---
	noMsg := "I'm sorry, but it seems we don't have any vehicles available that fit your request at the moment, " +
		"even with a wider search. Would you like to try searching with a different budget or car type?"

	return nil, "No Cars Found", noMsg
}

// ---- HTTP server ----

func main() {
	http.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("x-api-key")
		if apiKey != API_KEY {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "Unauthorized",
				"message": "A valid API key is required to access this endpoint.",
				"status":  401,
			})
			return
		}

		var req SearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		cars, err := getInventory()
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "InventoryLoadError",
				"message": err.Error(),
				"status":  500,
			})
			return
		}

		results, status, message := filterCars(req, cars)

		resp := map[string]interface{}{
			"status":  status,
			"results": results,
		}
		if message != "" {
			resp["message"] = message
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	log.Println("Go filter service running on :8080 (API key + CSV URL + cache)")
	log.Fatal(http.ListenAndServe(":8080", nil))
}