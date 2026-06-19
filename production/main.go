package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// --- Configuration ---
// INVYGO_API_KEY  : required, client auth for the /search endpoint.
// INVYGO_SHEET_URL: live Google Sheet CSV export URL (production).
// INVYGO_SHEET_FILE: local CSV path (dev/tests). Takes precedence over URL when set.
// TEST_API_KEY is a stable key used by the test suite; it is NOT used by the server.
const TEST_API_KEY = "test-api-key"

const CACHE_TTL = 10 * time.Minute

// Live production sheet (client keeps appending cars; always pull latest).
// Override with INVYGO_SHEET_URL, or use INVYGO_SHEET_FILE for offline/dev.
const defaultSheetURL = "https://docs.google.com/spreadsheets/d/122ODHukERmK36i0K5rI3o4lIgDAHDNL9AH6-EkKMgz4/export?format=csv"

func apiKey() string { return strings.TrimSpace(os.Getenv("INVYGO_API_KEY")) }

// --- Status contract (agent branches on these) ---
const (
	StatusIdeal        = "Ideal Match Found"     // exact request satisfied
	StatusAlternative  = "Alternative Found"     // same body_type, within budget (asymmetric) or STS closest
	StatusAboveBudget  = "Only Above Budget"     // same body_type exists, all above budget+200
	StatusBudgetNeeded = "Budget Needed"         // body_type known (STO/MONTHLY), no budget -> ask, range provided
	StatusNoBodyMatch  = "No Body Type Match"    // requested body_type has zero cars in city/product
	StatusNeedBodyType = "Need Body Type"        // cannot infer a body_type to anchor -> ask user
	StatusNoCars       = "No Cars Found"         // nothing in city (+product)
	StatusUpsell       = "Upsell Options Found"  // broad request, nothing matched, here is what we have
	StatusError        = "Error"
)

// Budget tolerance above the user's stated budget (asymmetric: below budget always allowed).
// Proportional so it scales across price tiers; at typical 2,000–2,500 prices this is ~+200–250
// (matching the old flat band), smaller for cheap cars and larger for premium. BUDGET_BAND_UP is
// retained for reference only — the live band is the percentage below.
const BUDGET_BAND_UP = 200.0
const BUDGET_BAND_PCT = 0.10

// bandCeiling is the highest price still considered "near budget" for fallback alternatives.
func bandCeiling(budget float64) float64 { return budget * (1 + BUDGET_BAND_PCT) }

// --- Data Models ---
type Car struct {
	Brand          string  `json:"brand_name"`
	Model          string  `json:"car_name_fixed"`
	Tier           string  `json:"tier"`
	City           string  `json:"city"`
	Showroom       string  `json:"showroom_name"`
	Area           string  `json:"area"`
	Year           int     `json:"manufacturing_year"`
	Trim           string  `json:"trim"`
	BodyType       string  `json:"body_type"`
	Color          string  `json:"color"`
	Product        string  `json:"product"` // STO | MONTHLY | STS
	BasePrice      float64 `json:"invygo_baseprice"`
	StarterFee     float64 `json:"starter_fee"`     // meaningful for STO only
	ContractLength int     `json:"sto_contract_length"`
	Condition      string  `json:"car_condition"`

	// Enriched
	PriceBasis string `json:"price_basis"` // "weekly" for STS, "monthly" otherwise
	IsHighSpec bool   `json:"is_high_spec"`
}

type GroupedResult struct {
	Description              string   `json:"description"`
	Brand                    string   `json:"brand"`
	Model                    string   `json:"model"`
	// Raw numeric fields are kept as Go fields (used internally for sorting and to
	// build the spoken forms) but are NOT serialized to the agent (`json:"-"`).
	// The agent must never see a raw digit it could re-render wrong (4820→8650,
	// 2025→2005); it sees ONLY the Arabic *_spoken words below.
	Year                     int      `json:"-"`
	Condition                string   `json:"condition"`
	BodyType                 string   `json:"body_type"`
	Tier                     string   `json:"tier"`
	Product                  string   `json:"product"`
	PriceBasis               string   `json:"price_basis"`
	BasePrice                float64  `json:"-"`
	StarterFee               float64  `json:"-"`                           // exact, STO only
	StarterFeeNote           string   `json:"starter_fee_note,omitempty"`  // 10% rule, MONTHLY/STS
	Area                     string   `json:"area"`
	City                     string   `json:"city"`
	AvailableColors          []string `json:"available_colors"`
	AvailableContractLengths []int    `json:"-"`
	Features                 string   `json:"features"`
	// Spoken (Saudi-Arabic words) forms of every numeric field. These are the
	// SINGLE SOURCE OF TRUTH for how the agent must voice numbers: the LLM must
	// copy these verbatim instead of converting digits itself (it corrupts them:
	// 2025→2005, 1950→2950, 36→33). Rendered deterministically here, at origin.
	YearSpoken                     string   `json:"year_spoken,omitempty"`
	BasePriceSpoken                string   `json:"base_price_spoken,omitempty"`
	StarterFeeSpoken               string   `json:"starter_fee_spoken,omitempty"`
	AvailableContractLengthsSpoken []string `json:"available_contract_lengths_spoken,omitempty"`
}

// flexFloat tolerates JSON numbers, numeric strings ("2500", "2,500"), null and "".
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" || s == `""` {
		*f = 0
		return nil
	}
	s = strings.Trim(s, `"`)
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*f = 0
		return nil // tolerate garbage as 0 rather than 400
	}
	*f = flexFloat(v)
	return nil
}

// flexInt tolerates JSON numbers, numeric strings ("2024", "2,024"), null and "".
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" || s == `""` {
		*f = 0
		return nil
	}
	s = strings.ReplaceAll(strings.Trim(s, `"`), ",", "")
	if s == "" {
		*f = 0
		return nil
	}
	if v, err := strconv.Atoi(s); err == nil {
		*f = flexInt(v)
		return nil
	}
	if fv, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(int(fv))
		return nil
	}
	*f = 0 // tolerate garbage as 0 rather than 400
	return nil
}

type SearchRequest struct {
	City      string    `json:"city"`
	Query     string    `json:"query"`
	Product   string    `json:"product"` // STO | MONTHLY | STS (optional)
	MaxPrice  flexFloat `json:"max_price"`
	MinPrice  flexFloat `json:"min_price"` // optional lower bound (range searches)
	MinYear   flexInt   `json:"min_year"`  // optional "2024 or newer"
	Color     string    `json:"color"`
	Condition string    `json:"condition"`
	Tier      string    `json:"tier"`
	BodyType  string    `json:"body_type"`
}

type SearchResult struct {
	Results []GroupedResult
	Status  string
	Message string
	Meta    map[string]interface{}
}

type InventoryCache struct {
	Cars        []Car
	BodyByModel map[string]string // "brand model" (lower) -> canonical body_type (majority vote)
	ExpiresAt   time.Time
	Mu          sync.RWMutex
}

var globalCache InventoryCache

// --- Cleaning helpers ---
func cleanPrice(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.TrimSpace(s)
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func cleanInt(s string) int {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSpace(s)
	i, _ := strconv.Atoi(s)
	return i
}

// normalizeBodyType fixes casing only; distinct types are preserved per business rule.
func normalizeBodyType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "suv":
		return "SUV"
	case "cuv":
		return "CUV"
	case "crossover":
		return "Crossover"
	case "hatchback":
		return "Hatchback"
	case "minivan":
		return "Minivan"
	case "van":
		return "Van"
	case "pickup", "pickup truck", "pick up":
		return "Pickup"
	case "sedan":
		return "Sedan"
	case "":
		return ""
	default:
		return strings.TrimSpace(s)
	}
}

func priceBasis(product string) string {
	if strings.EqualFold(product, "STS") {
		return "weekly"
	}
	return "monthly"
}

func inferBodyFromTier(tier string) string {
	t := strings.ToLower(tier)
	switch {
	case strings.Contains(t, "suv"):
		return "SUV"
	case strings.Contains(t, "sedan"):
		return "Sedan"
	case strings.Contains(t, "hatchback"):
		return "Hatchback"
	case strings.Contains(t, "pickup"):
		return "Pickup"
	case strings.Contains(t, "mpv"), strings.Contains(t, "minivan"):
		return "Minivan"
	case strings.Contains(t, "crossover"):
		return "Crossover"
	default:
		return ""
	}
}

func modelKey(brand, model string) string {
	return strings.ToLower(strings.TrimSpace(brand) + " " + strings.TrimSpace(model))
}

// --- Showroom area extraction ---
var dashSplit = regexp.MustCompile(`\s+-\s+`)
var parenStrip = regexp.MustCompile(`\([^)]*\)`)

var cityNoise = map[string]bool{
	"riyadh": true, "ritadh": true, "jeddah": true, "jdh": true, "dammam": true,
	"khobar": true, "al khobar": true, "madinah": true, "madina": true,
	"mecca": true, "makkah": true, "meeca": true, "makah": true,
	"taif": true, "arar": true, "turayf": true,
}

var partNoise = map[string]bool{
	"pickup": true, "pick-up": true, "pick up": true, "pick - up": true,
	"sto": true, "daily": true, "branch": true,
}

var wordNoise = map[string]bool{
	"daily": true, "pickup": true, "pick-up": true, "pick": true, "up": true,
	"sto": true, "branch": true,
	"riyadh": true, "ritadh": true, "jeddah": true, "jdh": true, "dammam": true,
	"khobar": true, "madinah": true, "madina": true, "mecca": true, "makkah": true,
	"meeca": true, "makah": true, "taif": true, "arar": true, "turayf": true,
}

func stripNonASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cleanArea returns the district/area from a showroom_name, suppressing the city
// prefix, "Pickup"/"STO"/"Daily"/"branch" markers, parentheticals and non-ASCII.
// Returns "" when no meaningful area can be extracted (agent then names only the city).
func cleanArea(showroom, _ string) string {
	s := strings.TrimSpace(showroom)
	if s == "" {
		return ""
	}
	s = parenStrip.ReplaceAllString(s, " ")
	s = stripNonASCII(s)
	s = strings.TrimSpace(s)

	parts := dashSplit.Split(s, -1)
	var kept []string
	for _, p := range parts {
		pp := strings.TrimSpace(p)
		if pp == "" {
			continue
		}
		lp := strings.ToLower(pp)
		if cityNoise[lp] || partNoise[lp] || isNumeric(pp) {
			continue
		}
		kept = append(kept, pp)
	}
	// Word-level pass catches no-dash forms like "Daily Sulimaniyah Riyadh".
	var words []string
	for _, w := range strings.Fields(strings.Join(kept, " ")) {
		if wordNoise[strings.ToLower(w)] {
			continue
		}
		words = append(words, w)
	}
	return strings.TrimSpace(strings.Join(words, " "))
}

// --- Loading ---
func loadCars() ([]Car, error) {
	if f := strings.TrimSpace(os.Getenv("INVYGO_SHEET_FILE")); f != "" {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		defer fh.Close()
		return parseCSV(fh)
	}
	url := strings.TrimSpace(os.Getenv("INVYGO_SHEET_URL"))
	if url == "" {
		url = defaultSheetURL
	}
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseCSV(resp.Body)
}

func parseCSV(r io.Reader) ([]Car, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	return parseRecords(records), nil
}

// parseRecords maps rows by HEADER NAME (robust to column reordering / dirty columns).
func parseRecords(records [][]string) []Car {
	if len(records) < 2 {
		return nil
	}
	idx := map[string]int{}
	for i, h := range records[0] {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(row []string, name string) string {
		if i, ok := idx[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}

	var loaded []Car
	for _, row := range records[1:] {
		brand := get(row, "brand_name_fixed")
		model := get(row, "car_name_fixed")
		city := canonicalCity(get(row, "city"))
		product := strings.ToUpper(get(row, "product"))
		// Skip rows missing the essentials.
		if brand == "" || model == "" || city == "" || product == "" {
			continue
		}
		c := Car{
			Brand:          brand,
			Model:          model,
			Tier:           get(row, "tier"),
			City:           city,
			Showroom:       get(row, "showroom_name"),
			Year:           cleanInt(get(row, "manufacturing_year")),
			Trim:           get(row, "trim"),
			BodyType:       normalizeBodyType(get(row, "body_type")),
			Color:          get(row, "color"),
			Product:        product,
			BasePrice:      cleanPrice(get(row, "invygo_baseprice")),
			StarterFee:     cleanPrice(get(row, "starter_fee")),
			ContractLength: cleanInt(get(row, "sto_contract_length")),
			Condition:      strings.ToUpper(get(row, "car_condition")),
		}
		c.Area = cleanArea(c.Showroom, c.City)
		c.PriceBasis = priceBasis(product)
		tu := strings.ToUpper(c.Trim)
		if strings.Contains(tu, "SMART") || strings.Contains(tu, "DELUXE") ||
			strings.Contains(tu, "PREMIUM") || strings.Contains(tu, "FULL") {
			c.IsHighSpec = true
		}
		loaded = append(loaded, c)
	}
	return loaded
}

// buildBodyByModel resolves one canonical body_type per model via majority vote,
// so body_type inference for fallbacks is deterministic and data-true.
func buildBodyByModel(cars []Car) map[string]string {
	counts := map[string]map[string]int{}
	for _, c := range cars {
		if c.BodyType == "" {
			continue
		}
		k := modelKey(c.Brand, c.Model)
		if counts[k] == nil {
			counts[k] = map[string]int{}
		}
		counts[k][c.BodyType]++
	}
	out := map[string]string{}
	for k, m := range counts {
		bts := make([]string, 0, len(m))
		for bt := range m {
			bts = append(bts, bt)
		}
		sort.Strings(bts) // deterministic tie-break
		best, bestN := "", -1
		for _, bt := range bts {
			if m[bt] > bestN {
				bestN, best = m[bt], bt
			}
		}
		out[k] = best
	}
	return out
}

func getInventory() ([]Car, map[string]string, error) {
	globalCache.Mu.RLock()
	cars, bm, exp := globalCache.Cars, globalCache.BodyByModel, globalCache.ExpiresAt
	globalCache.Mu.RUnlock()

	if len(cars) > 0 && time.Now().Before(exp) {
		return cars, bm, nil
	}
	fresh, err := loadCars()
	if err != nil {
		if len(cars) > 0 {
			return cars, bm, nil // serve stale on refresh failure
		}
		return nil, nil, err
	}
	bmap := buildBodyByModel(fresh)
	globalCache.Mu.Lock()
	globalCache.Cars, globalCache.BodyByModel = fresh, bmap
	globalCache.ExpiresAt = time.Now().Add(CACHE_TTL)
	globalCache.Mu.Unlock()
	return fresh, bmap, nil
}

// --- Small list helpers ---
func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func priceRange(cars []Car) (min, max float64) {
	for _, c := range cars {
		if c.BasePrice <= 0 {
			continue
		}
		if min == 0 || c.BasePrice < min {
			min = c.BasePrice
		}
		if c.BasePrice > max {
			max = c.BasePrice
		}
	}
	return
}

// bodyTypesPresent lists the distinct body types available in a (priced) set, sorted.
func bodyTypesPresent(cars []Car) []string {
	set := map[string]bool{}
	for _, c := range cars {
		if c.BodyType != "" {
			set[c.BodyType] = true
		}
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// cityRegion groups service cities so a "no stock here" can suggest the nearest cities first.
var cityRegion = map[string]string{
	"riyadh": "central",
	"jeddah": "west", "mecca": "west", "madinah": "west", "taif": "west",
	"dammam": "east", "al khobar": "east", "al hasa": "east",
	"arar": "north", "turayf": "north",
	"abha": "south", "khamis": "south",
}

// citiesWithStock returns service cities (excluding exclCity) that have priced stock for the
// product, ordered same-region-first so the agent can suggest the nearest alternative.
func citiesWithStock(inv []Car, product, exclCity string) []string {
	set := map[string]bool{}
	for _, c := range inv {
		if c.BasePrice <= 0 {
			continue
		}
		if product != "" && !strings.EqualFold(c.Product, product) {
			continue
		}
		if strings.EqualFold(c.City, exclCity) {
			continue
		}
		set[c.City] = true
	}
	reg := cityRegion[strings.ToLower(strings.TrimSpace(exclCity))]
	var same, other []string
	for c := range set {
		if reg != "" && cityRegion[strings.ToLower(c)] == reg {
			same = append(same, c)
		} else {
			other = append(other, c)
		}
	}
	sort.Strings(same)
	sort.Strings(other)
	return append(same, other...)
}

// productsInCity returns plans (excluding exclProduct) that have priced stock in the city.
func productsInCity(inv []Car, city, exclProduct string) []string {
	set := map[string]bool{}
	for _, c := range inv {
		if c.BasePrice <= 0 || !strings.EqualFold(c.City, city) {
			continue
		}
		if exclProduct != "" && strings.EqualFold(c.Product, exclProduct) {
			continue
		}
		set[c.Product] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// priceLadder returns a representative spread (cheapest, mid points, most expensive) of up to n
// options, so a no-budget customer anchors on real prices instead of being asked for a budget.
func priceLadder(g []GroupedResult, n int) []GroupedResult {
	sortByPriceAsc(g)
	if len(g) <= n || n < 2 {
		return capN(g, n)
	}
	out := make([]GroupedResult, 0, n)
	used := map[int]bool{}
	for i := 0; i < n; i++ {
		idx := i * (len(g) - 1) / (n - 1)
		if used[idx] {
			continue
		}
		used[idx] = true
		out = append(out, g[idx])
	}
	return out
}

// withCitySuggestion appends a "I do have them in X, Y" tail when alternatives exist.
func withCitySuggestion(msg string, cities []string) string {
	if len(cities) == 0 {
		return msg
	}
	show := cities
	if len(show) > 3 {
		show = show[:3]
	}
	return msg + " I do have them in " + strings.Join(show, ", ") + "."
}

func tierMatches(carTier, reqTier string) bool {
	if strings.EqualFold(carTier, reqTier) {
		return true
	}
	return strings.Contains(strings.ToLower(carTier), strings.ToLower(reqTier))
}

// lookupBodyByQuery returns a body_type only when the query maps to a single
// distinct body_type across known models (brand-only / ambiguous -> "").
func lookupBodyByQuery(bodyByModel map[string]string, query string) string {
	if bt, ok := bodyByModel[query]; ok {
		return bt
	}
	set := map[string]bool{}
	for k, bt := range bodyByModel {
		model := k
		if sp := strings.Index(k, " "); sp >= 0 {
			model = k[sp+1:]
		}
		if strings.Contains(k, query) || (model != "" && strings.Contains(query, model)) {
			set[bt] = true
		}
	}
	if len(set) == 1 {
		for bt := range set {
			return bt
		}
	}
	return ""
}

// globalAvailability reports other cities / products where a queried car exists.
func globalAvailability(inv []Car, query, exclCity, exclProduct string) (cities, products []string) {
	cs, ps := map[string]bool{}, map[string]bool{}
	for _, c := range inv {
		text := strings.ToLower(c.Brand + " " + c.Model)
		if !strings.Contains(text, query) {
			continue
		}
		if !strings.EqualFold(c.City, exclCity) {
			cs[c.City] = true
		}
		if exclProduct != "" && !strings.EqualFold(c.Product, exclProduct) {
			ps[c.Product] = true
		}
	}
	for c := range cs {
		cities = append(cities, c)
	}
	for p := range ps {
		products = append(products, p)
	}
	sort.Strings(cities)
	sort.Strings(products)
	return
}

// tierAvailability reports other cities / products where a requested tier/segment exists.
func tierAvailability(inv []Car, tier, exclCity, exclProduct string) (cities, products []string) {
	cs, ps := map[string]bool{}, map[string]bool{}
	for _, c := range inv {
		if c.BasePrice <= 0 || !tierMatches(c.Tier, tier) {
			continue
		}
		if !strings.EqualFold(c.City, exclCity) {
			cs[c.City] = true
		}
		if exclProduct != "" && !strings.EqualFold(c.Product, exclProduct) {
			ps[c.Product] = true
		}
	}
	for c := range cs {
		cities = append(cities, c)
	}
	for p := range ps {
		products = append(products, p)
	}
	sort.Strings(cities)
	sort.Strings(products)
	return
}

// --- Sorting / capping ---
func sortByPriceAsc(g []GroupedResult)  { sort.SliceStable(g, func(i, j int) bool { return g[i].BasePrice < g[j].BasePrice }) }
func sortByPriceDesc(g []GroupedResult) { sort.SliceStable(g, func(i, j int) bool { return g[i].BasePrice > g[j].BasePrice }) }

func sortByCloseness(g []GroupedResult, budget float64) {
	abs := func(x float64) float64 {
		if x < 0 {
			return -x
		}
		return x
	}
	sort.SliceStable(g, func(i, j int) bool {
		di, dj := abs(g[i].BasePrice-budget), abs(g[j].BasePrice-budget)
		if di == dj {
			return g[i].BasePrice < g[j].BasePrice
		}
		return di < dj
	})
}

func capN(g []GroupedResult, n int) []GroupedResult {
	if len(g) > n {
		return g[:n]
	}
	return g
}

func sortAndCap(g []GroupedResult, budget float64, isSTS bool) []GroupedResult {
	// ALL plans (STO / MONTHLY / STS) — ALWAYS cheapest-first. A max_price has already
	// filtered out anything above budget, so it only caps the ceiling; within the band we
	// always lead with the cheapest. Uniform regardless of plan or budget, so a "cheapest"
	// (or any budget) request always surfaces the genuinely lowest cars and never caps the
	// cheapest off behind pricier in-band ones. (Fixes client feedback #3.)
	_ = isSTS
	_ = budget
	sortByPriceAsc(g)
	return capN(g, 4)
}

// --- Core filter / fallback matrix ---
// normalizeCity maps Arabic city names and common variants to the canonical English
// sheet name, so an Arabic-speaking agent passing "الرياض" still matches "Riyadh".
// Exact (trimmed, lower) match only — never substring, to avoid false hits.
func normalizeCity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "riyadh", "الرياض", "رياض":
		return "Riyadh"
	case "jeddah", "jedah", "jaddah", "جدة", "جده":
		return "Jeddah"
	case "dammam", "الدمام", "دمام":
		return "Dammam"
	case "al khobar", "alkhobar", "khobar", "الخبر", "خبر":
		return "Al Khobar"
	case "madinah", "medina", "al madinah", "المدينة", "المدينه", "مدينة":
		return "Madinah"
	case "mecca", "makkah", "makah", "meeca", "مكة", "مكه":
		return "Mecca"
	case "taif", "al taif", "الطائف", "طائف":
		return "Taif"
	case "arar", "عرعر":
		return "Arar"
	case "turayf", "turaif", "طريف":
		return "Turayf"
	case "abha", "أبها", "ابها":
		return "Abha"
	case "al hasa", "alhasa", "al-hasa", "hasa", "hofuf", "الأحساء", "الاحساء", "الحسا", "أحساء":
		return "Al Hasa"
	case "khamis", "khamis mushait", "خميس", "خميس مشيط":
		return "Khamis"
	}
	return strings.TrimSpace(s)
}

// cityCanon folds free-typed inventory city spellings to one canonical English name, so a
// client typo/variant (e.g. "Makah" → "Mecca") doesn't split one city into two buckets.
var cityCanon = map[string]string{
	"makah": "Mecca", "makkah": "Mecca", "meeca": "Mecca",
}

// canonicalCity normalizes a raw inventory city value before it is stored on a Car.
func canonicalCity(s string) string {
	t := strings.TrimSpace(s)
	if c, ok := cityCanon[strings.ToLower(t)]; ok {
		return c
	}
	return t
}

// allServiceCities returns the live distinct list of cities that currently have priced stock,
// derived straight from inventory — the source of truth for "which cities do you serve?".
func allServiceCities(inv []Car) []string {
	set := map[string]bool{}
	for _, c := range inv {
		if c.BasePrice > 0 && strings.TrimSpace(c.City) != "" {
			set[c.City] = true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func filterCars(req SearchRequest, inv []Car, bodyByModel map[string]string) SearchResult {
	city := normalizeCity(req.City)
	product := strings.ToUpper(strings.TrimSpace(req.Product))
	cond := strings.ToUpper(strings.TrimSpace(req.Condition))
	query := strings.ToLower(strings.TrimSpace(req.Query))
	color := strings.TrimSpace(req.Color)
	tier := strings.TrimSpace(req.Tier)
	body := normalizeBodyType(req.BodyType)
	budget := float64(req.MaxPrice)
	minPrice := float64(req.MinPrice)
	minYear := int(req.MinYear)
	isSTS := product == "STS"
	empty := []GroupedResult{}
	meta := map[string]interface{}{}
	priceOK := func(p float64) bool { return minPrice <= 0 || p >= minPrice }

	// PHASE 0 — base set. HARD gates only: city, product, priced. (Condition is now a SOFT
	// preference applied in Phase 1 and relaxed in fallback, so "used" never dead-ends when
	// only new cars exist.)
	var base []Car
	for _, c := range inv {
		if !strings.EqualFold(c.City, city) {
			continue
		}
		if product != "" && !strings.EqualFold(c.Product, product) {
			continue
		}
		if c.BasePrice <= 0 {
			continue // never present an unpriced car (cannot be quoted)
		}
		base = append(base, c)
	}
	if len(base) == 0 {
		// A3 — never a bare dead-end: point to where stock exists.
		// MODEL-SCOPED: if the customer asked for a specific model/brand, only point to
		// cities where THAT car actually exists — never plan-level cities (that would
		// imply the requested model is available elsewhere when it isn't). Fall back to
		// plan-level stock only when no specific model was requested.
		var cs []string
		if strings.TrimSpace(query) != "" {
			cs, _ = globalAvailability(inv, query, city, product)
		} else {
			cs = citiesWithStock(inv, product, city)
		}
		if len(cs) > 0 {
			meta["also_in_cities"] = cs
		}
		if ps := productsInCity(inv, city, product); len(ps) > 0 {
			meta["also_in_products"] = ps
		}
		return SearchResult{Results: empty, Status: StatusNoCars, Meta: meta,
			Message: withCitySuggestion("We currently have no cars available in "+city+planLabel(product)+".", cs)}
	}

	// PHASE 1 — ideal match. All preferences applied here (condition + min_year + price range).
	var ideal []Car
	for _, c := range base {
		if color != "" && !strings.EqualFold(c.Color, color) {
			continue
		}
		if cond != "" && !strings.EqualFold(c.Condition, cond) {
			continue
		}
		if minYear > 0 && c.Year < minYear {
			continue
		}
		if tier != "" && !tierMatches(c.Tier, tier) {
			continue
		}
		if body != "" && !strings.EqualFold(c.BodyType, body) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(c.Brand+" "+c.Model+" "+c.BodyType), query) {
			continue
		}
		if !priceOK(c.BasePrice) {
			continue
		}
		if !isSTS && budget > 0 && c.BasePrice > budget {
			continue
		}
		ideal = append(ideal, c)
	}
	if len(ideal) > 0 {
		return SearchResult{Results: sortAndCap(groupResults(ideal), budget, isSTS),
			Status: StatusIdeal, Message: "I have what you're looking for."}
	}

	// Enrichment: where else a specifically-requested car exists.
	if query != "" {
		cities, products := globalAvailability(inv, query, city, product)
		if len(cities) > 0 {
			meta["also_in_cities"] = cities
		}
		if len(products) > 0 {
			meta["also_in_products"] = products
		}
	}

	// Anchor body_type for fallback: explicit > tier-derived > model-derived.
	target := body
	if target == "" && tier != "" {
		target = inferBodyFromTier(tier)
	}
	if target == "" && query != "" {
		target = lookupBodyByQuery(bodyByModel, query)
	}

	if target == "" {
		// A3 — always surface the body types we DO carry here.
		if bts := bodyTypesPresent(base); len(bts) > 0 {
			meta["available_body_types"] = bts
		}
		// A segment/tier (e.g. bare "Luxury") with no in-product match and no
		// inferable body_type: report unavailable + where it exists (no handoff).
		if tier != "" {
			cities, products := tierAvailability(inv, tier, city, product)
			if len(cities) > 0 {
				meta["also_in_cities"] = cities
			}
			if len(products) > 0 {
				meta["also_in_products"] = products
			}
			return SearchResult{Results: empty, Status: StatusNoBodyMatch, Meta: meta,
				Message: "I don't have " + tier + " options in " + city + planLabel(product) + " right now."}
		}
		// Unknown specific model: ask which body type to anchor on (we still surface what we have).
		if query != "" {
			return SearchResult{Results: empty, Status: StatusNeedBodyType, Meta: meta,
				Message: "Which body type would you like — for example sedan, SUV, or hatchback?"}
		}
		// Truly broad ("show me cars"): a representative price-laddered sample.
		return SearchResult{Results: priceLadder(groupResults(base), 4), Status: StatusUpsell, Meta: meta,
			Message: "Here are some of our available options."}
	}
	meta["target_body_type"] = target

	// Candidates of the anchor body_type. Firm numeric constraints (min_year, min_price) kept;
	// descriptive ones (color, condition) relaxed and reported via relaxed_filters.
	var cand []Car
	for _, c := range base {
		if !strings.EqualFold(c.BodyType, target) {
			continue
		}
		if minYear > 0 && c.Year < minYear {
			continue
		}
		if !priceOK(c.BasePrice) {
			continue
		}
		cand = append(cand, c)
	}
	if len(cand) == 0 {
		if bts := bodyTypesPresent(base); len(bts) > 0 {
			meta["available_body_types"] = bts
		}
		return SearchResult{Results: empty, Status: StatusNoBodyMatch, Meta: meta,
			Message: "I don't have any " + target + " options in " + city + planLabel(product) + " right now."}
	}

	// Report descriptive filters we broadened past in this fallback.
	var relaxed []string
	if cond != "" {
		relaxed = append(relaxed, "condition")
	}
	if color != "" {
		relaxed = append(relaxed, "color")
	}
	if len(relaxed) > 0 {
		meta["relaxed_filters"] = relaxed
	}

	// STS: no band — closest weekly price (budget optional).
	if isSTS {
		return SearchResult{Results: sortAndCap(groupResults(cand), budget, true),
			Status: StatusAlternative, Meta: meta,
			Message: "Here are " + target + " options I can offer."}
	}

	// STO / MONTHLY, NO budget (A1+B7) — never dead-end on budget: present a price-laddered
	// spread (cheapest → premium) so the customer reacts to real prices; the budget ask is a
	// soft, optional hint, not a gate.
	if budget <= 0 {
		min, max := priceRange(cand)
		meta["price_min"], meta["price_max"] = min, max
		g := priceLadder(groupResults(cand), 4)
		// Prices in the message as Arabic WORDS, never digits the agent could misread.
		return SearchResult{Results: g, Status: StatusAlternative, Meta: meta,
			Message: fmt.Sprintf("Here are a few %s options from %s to %s Saudi Riyaals — want me to narrow to a price, or shall I tell you about these?", target, arMoneyWords(min), arMoneyWords(max))}
	}

	// STO / MONTHLY with budget: asymmetric proportional band (cheaper always allowed).
	var inBand []Car
	for _, c := range cand {
		if c.BasePrice <= bandCeiling(budget) {
			inBand = append(inBand, c)
		}
	}
	if len(inBand) > 0 {
		g := groupResults(inBand)
		sortByCloseness(g, budget)
		return SearchResult{Results: capN(g, 4), Status: StatusAlternative, Meta: meta,
			Message: "Here are " + target + " options close to your budget."}
	}
	// Everything is above the band ceiling.
	min, _ := priceRange(cand)
	meta["cheapest"] = min
	g := capN(sortedAsc(groupResults(cand)), 4)
	return SearchResult{Results: g, Status: StatusAboveBudget, Meta: meta,
		Message: fmt.Sprintf("The %s options I have start around %s Saudi Riyaals, a bit above your budget.", target, arMoneyWords(min))}
}

func sortedAsc(g []GroupedResult) []GroupedResult {
	sortByPriceAsc(g)
	return g
}

func planLabel(product string) string {
	switch strings.ToUpper(product) {
	case "STO":
		return " on the Subscribe to Own plan"
	case "MONTHLY":
		return " on the Monthly Subscription plan"
	case "STS":
		return " for short-term rental"
	default:
		return ""
	}
}

// --- Arabic number rendering (Saudi dialect) -------------------------------
// SINGLE SOURCE OF TRUTH for how numbers are spoken. The LLM must copy the
// resulting *_spoken strings verbatim instead of converting digits itself — it
// corrupts them in Arabic (2025→2005, 1950→2950, 36→33, 570→700). Rendering the
// exact integer to words here, at origin, makes the spoken form impossible to
// drift from the value. Word forms match the Saudi dialect already in use
// (ألفين, مية/ميتين/ثلاثمية, units-before-tens joined with «و»).
var arOnes = []string{"", "واحد", "اثنين", "ثلاثة", "أربعة", "خمسة", "ستة", "سبعة", "ثمانية", "تسعة"}
var arTeens = []string{"عشرة", "أحد عشر", "اثنا عشر", "ثلاثة عشر", "أربعة عشر", "خمسة عشر", "ستة عشر", "سبعة عشر", "ثمانية عشر", "تسعة عشر"}
var arTens = []string{"", "", "عشرين", "ثلاثين", "أربعين", "خمسين", "ستين", "سبعين", "ثمانين", "تسعين"}
var arHundreds = []string{"", "مية", "ميتين", "ثلاثمية", "أربعمية", "خمسمية", "ستمية", "سبعمية", "ثمنمية", "تسعمية"}

func arTwoDigits(n int) string {
	switch {
	case n == 0:
		return ""
	case n < 10:
		return arOnes[n]
	case n < 20:
		return arTeens[n-10]
	}
	u, t := n%10, n/10
	if u == 0 {
		return arTens[t]
	}
	return arOnes[u] + " و" + arTens[t]
}

func arThousands(n int) string {
	switch {
	case n == 1:
		return "ألف"
	case n == 2:
		return "ألفين"
	case n >= 3 && n <= 10:
		return arTwoDigits(n) + " آلاف" // arTwoDigits renders 10 as "عشرة"; arOnes[10] was out of range
	}
	return arNumberWords(n) + " ألف"
}

// arNumberWords renders 0..999999 as spoken Saudi-Arabic words.
func arNumberWords(n int) string {
	if n < 0 || n > 999999 {
		return fmt.Sprintf("%d", n)
	}
	if n == 0 {
		return "صفر"
	}
	parts := []string{}
	th, rest := n/1000, n%1000
	if th > 0 {
		parts = append(parts, arThousands(th))
	}
	h, two := rest/100, rest%100
	if h > 0 {
		parts = append(parts, arHundreds[h])
	}
	if two > 0 {
		parts = append(parts, arTwoDigits(two))
	}
	return strings.Join(parts, " و")
}

// arMoneyWords renders a whole-riyal price as spoken words.
func arMoneyWords(f float64) string { return arNumberWords(int(f + 0.5)) }

func groupResults(cars []Car) []GroupedResult {
	grouped := map[string]*GroupedResult{}
	var order []string
	for _, c := range cars {
		key := fmt.Sprintf("%s|%s|%d|%s|%s|%.0f", c.Brand, c.Model, c.Year, c.Condition, c.Product, c.BasePrice)
		g, ok := grouped[key]
		if !ok {
			feat := ""
			if c.IsHighSpec {
				feat = "High Spec"
			}
			sf, note := 0.0, ""
			if strings.EqualFold(c.Product, "STO") {
				sf = c.StarterFee
			} else {
				note = "A one-time starter fee of 10% of the " + priceBasis(c.Product) + " amount applies."
			}
			// Description carries the year as Arabic WORDS (not digits), so even this
			// human-readable label can't be misread as a number by the agent.
			desc := strings.TrimSpace(c.Brand + " " + c.Model)
			if c.Year > 0 {
				desc += " " + arNumberWords(c.Year)
			}
			g = &GroupedResult{
				Description:     desc,
				Brand:           c.Brand,
				Model:           c.Model,
				Year:            c.Year,
				Condition:       c.Condition,
				BodyType:        c.BodyType,
				Tier:            c.Tier,
				Product:         c.Product,
				PriceBasis:      c.PriceBasis,
				BasePrice:       c.BasePrice,
				StarterFee:      sf,
				StarterFeeNote:  note,
				Area:            c.Area,
				City:            c.City,
				AvailableColors: []string{},
				Features:        feat,
			}
			grouped[key] = g
			order = append(order, key)
		}
		if c.Color != "" && !containsFold(g.AvailableColors, c.Color) {
			g.AvailableColors = append(g.AvailableColors, c.Color)
		}
		if strings.EqualFold(c.Product, "STO") && c.ContractLength > 0 && !containsInt(g.AvailableContractLengths, c.ContractLength) {
			g.AvailableContractLengths = append(g.AvailableContractLengths, c.ContractLength)
		}
	}
	results := make([]GroupedResult, 0, len(order))
	for _, k := range order {
		g := grouped[k]
		sort.Ints(g.AvailableContractLengths)
		// Deterministic spoken forms — the agent voices numbers by copying these.
		if g.Year > 0 {
			g.YearSpoken = arNumberWords(g.Year)
		}
		g.BasePriceSpoken = arMoneyWords(g.BasePrice)
		if g.StarterFee > 0 {
			g.StarterFeeSpoken = arMoneyWords(g.StarterFee)
		}
		if len(g.AvailableContractLengths) > 0 {
			cls := make([]string, 0, len(g.AvailableContractLengths))
			for _, n := range g.AvailableContractLengths {
				cls = append(cls, arNumberWords(n))
			}
			g.AvailableContractLengthsSpoken = cls
		}
		results = append(results, *g)
	}
	return results
}

// --- HTTP ---
func SearchHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	key := apiKey()
	if key == "" {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": StatusError, "message": "Server is not configured (missing INVYGO_API_KEY)."})
		return
	}
	if r.Header.Get("x-api-key") != key {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "Unauthorized"})
		return
	}
	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "Invalid request body"})
		return
	}
	if strings.TrimSpace(req.City) == "" {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": StatusError, "message": "City is mandatory."})
		return
	}
	cars, bodyMap, err := getInventory()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": StatusError, "message": "Inventory is temporarily unavailable. Please try again shortly.", "error": err.Error()})
		return
	}
	sr := filterCars(req, cars, bodyMap)
	resp := map[string]interface{}{"status": sr.Status, "message": sr.Message, "results": sr.Results}
	for k, v := range sr.Meta {
		resp[k] = v
	}
	// Live serviceable-cities list (from inventory), so the agent can answer "which cities do
	// you serve?" and suggest real alternatives without relying on a hardcoded list.
	resp["service_cities"] = allServiceCities(cars)
	// Always surface the cheapest *presentable* price as a grounded hint, on ANY status
	// that returned cars (not just Only-Above-Budget). This lets the agent answer an
	// explicit "what's your cheapest car?" from a real token even when the budget sort
	// put the most expensive car first — the value is the min over what we actually return,
	// so it's always a car the agent can present.
	if _, ok := resp["cheapest"]; !ok && len(sr.Results) > 0 {
		min := sr.Results[0].BasePrice
		for _, g := range sr.Results {
			if g.BasePrice < min {
				min = g.BasePrice
			}
		}
		resp["cheapest"] = min
	}
	// Spoken forms for the numeric hints the agent quotes (e.g. "starts around ...").
	// Emit ONLY the Arabic words and delete the raw digit so the agent can't read it.
	for _, fld := range []string{"cheapest", "price_min", "price_max"} {
		if fv, ok := resp[fld].(float64); ok {
			resp[fld+"_spoken"] = arMoneyWords(fv)
			delete(resp, fld)
		}
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// CitiesHandler returns the live serviceable-cities list, derived straight from inventory. No
// request body is needed. It lets the agent fetch the current cities before naming them, or before
// deciding whether a customer's city is served — so nothing about cities is ever hardcoded.
func CitiesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if apiKey() == "" {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": StatusError, "message": "Server is not configured (missing INVYGO_API_KEY)."})
		return
	}
	if r.Header.Get("x-api-key") != apiKey() {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "Unauthorized"})
		return
	}
	cars, _, err := getInventory()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": StatusError, "message": "Inventory is temporarily unavailable. Please try again shortly."})
		return
	}
	cities := allServiceCities(cars)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"cities":  cities,
		"count":   len(cities),
		"message": "These are the cities Invygo currently serves (live from inventory).",
	})
}

func main() {
	if apiKey() == "" {
		log.Fatal("Missing required environment variable: INVYGO_API_KEY")
	}
	if _, _, err := getInventory(); err != nil {
		log.Println("Init Error:", err)
	} else {
		log.Println("Inventory Loaded.")
	}
	// Distinct default port so this runs alongside the old pilot service (:8080).
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8081"
	}
	http.HandleFunc("/search", SearchHandler)
	http.HandleFunc("/cities", CitiesHandler)
	log.Println("Service running on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
