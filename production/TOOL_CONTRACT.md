# `filter_invygo_car` — Tool & Response Contract (production / multi-plan)

This documents the rewritten inventory service in `main.go` so the prompt phase and the
convoi platform tool config stay in sync. Source of truth: the live Google Sheet
(15-column "Invygo Inventory for ConvoiAi"). 2,371 rows in the current snapshot.

## Deployment / env

| Var | Purpose |
|-----|---------|
| `INVYGO_API_KEY` | required; `x-api-key` checked on every request |
| `INVYGO_SHEET_URL` | live Google Sheet **CSV export** URL (production) |
| `INVYGO_SHEET_FILE` | local CSV path; overrides URL (dev/tests) |
| `PORT` | listen port; **default 8081** (old pilot keeps 8080) |

Endpoint: `POST /search`. Cache TTL 60 min, serves stale on refresh failure.

**Coexistence with the old pilot:** the old STO-only service stays at the repo root
(port 8080, `https://go.convoi.ai/invygo/search`) and keeps serving the live pilot
agents unchanged. This NEW service is a separate binary under `production/` on port
**8081**; expose it via a NEW reverse-proxy route, e.g. `https://go.convoi.ai/invygo-v2/search`.
Build: `go build -o invygo-prod ./production`.

## Tool parameter schema (for the convoi platform `filter_invygo_car`)

```json
{
  "type": "object",
  "required": ["city"],
  "properties": {
    "city":      { "type": "string", "enum": ["Riyadh","Jeddah","Dammam","Al Khobar","Madinah","Mecca","Taif","Arar","Turayf"], "description": "Pickup city. MANDATORY." },
    "product":   { "type": "string", "enum": ["STO","MONTHLY","STS",""], "description": "Plan. Each squad agent sends its own: STO / MONTHLY / STS (weekly rental)." },
    "query":     { "type": "string", "description": "Specific brand or model only (e.g. 'Toyota','Yaris'). NOT body types/tiers." },
    "max_price": { "type": "number", "description": "User's budget. Monthly amount for STO/MONTHLY; weekly for STS (optional for STS)." },
    "body_type": { "type": "string", "enum": ["Sedan","SUV","Crossover","CUV","Hatchback","Minivan","Pickup","Van",""], "description": "Body shape. Distinct values; not merged." },
    "tier":      { "type": "string", "description": "ONLY for explicit segment/luxury requests (e.g. 'Luxury','Midsize Sedan'). Not for recommendations." },
    "color":     { "type": "string" },
    "condition": { "type": "string", "enum": ["NEW","USED",""] }
  }
}
```

Notes:
- `tier` is kept **only** for direct segment/luxury queries. Recommendations/fallbacks
  use `body_type` + budget, never tier.
- Send numbers as numbers; the service also tolerates `"2,500"` / `null`.
- Request template stays `{"city":"${city}", ...}`; response root is `{status,message,results,...}`.

## Status contract (the agent branches on `status`)

| `status` | Meaning | Agent action |
|----------|---------|--------------|
| `Ideal Match Found` | request satisfied in-city/in-product | present top 2 (hold rest) |
| `Alternative Found` | requested car/exact not found → **same body_type within budget+200** (or STS closest weekly) | "couldn't find X, here are similar [body]" |
| `Only Above Budget` | same body_type exists but all > budget+200 | quote `cheapest`, offer to adjust |
| `Budget Needed` | body_type known (STO/MONTHLY), no budget | ask budget using `price_min`–`price_max` range |
| `No Body Type Match` | requested body_type/tier has nothing here | say none available; use `also_in_*` to inform where it is (NO handoff) |
| `Need Body Type` | unknown model, no anchor | ask "sedan, SUV, …?" |
| `No Cars Found` | nothing in city (+product) | apologise; offer another city/plan |
| `Upsell Options Found` | broad "show me cars" | present a small sampler |
| `Error` | bad/missing city or server | recover |

### Root meta fields (present when relevant)
- `target_body_type` — the body_type the fallback anchored on.
- `price_min`, `price_max` — range for `Budget Needed`.
- `cheapest` — for `Only Above Budget`.
- `also_in_cities`, `also_in_products` — where a requested car/tier exists elsewhere
  (drives the travel flow / "available on another plan" line; **inform only, no handoff**).

## Per-car result fields

```jsonc
{
  "description": "Toyota Yaris 2026",
  "brand": "Toyota", "model": "Yaris", "year": 2026,
  "condition": "NEW", "body_type": "Sedan", "tier": "Entry level Sedan",
  "product": "STO",                 // STO | MONTHLY | STS
  "price_basis": "monthly",         // "weekly" for STS, else "monthly"
  "base_price": 2500,               // invygo_baseprice (per price_basis)
  "starter_fee": 2500,              // EXACT — STO only
  "starter_fee_note": "A one-time starter fee of 10% of the weekly amount applies.", // MONTHLY/STS only
  "area": "As Sulimaniyah",         // district from showroom_name (city/Pickup/STO/Daily stripped); "" → name only the city
  "city": "Riyadh",
  "available_colors": ["Silver","Gray","White"],
  "available_contract_lengths": [9,36], // STO only (months)
  "features": "High Spec"
}
```

### Pricing rules the agent must follow (no agent math)
- **STO** → quote `base_price` as the monthly installment **and** the exact `starter_fee`.
- **MONTHLY** → quote `base_price` monthly; for starter fee, state the **rule** from
  `starter_fee_note` (10% of the monthly amount). Do not compute the figure.
- **STS** → `base_price` is the **weekly** price; quote it for both weekly and daily
  asks. For exact daily/multi-day totals, defer to the app. Starter fee = 10% rule note.
- Unpriced rows (`base_price<=0`) are never returned.

## Behaviour locked with client (2026-06-04)
- Asymmetric band: allow ≤ budget, cap at **budget+200**, rank by closeness.
- STS: no band; sort by closest weekly price; budget optional.
- Cross-product/city: **inform only**, then offer same-product alternatives. No auto-handoff.
- body_type inference is data-driven (majority vote per model); unknown model → ask.
- Tier kept for direct luxury/segment queries; excluded from recommendations.
