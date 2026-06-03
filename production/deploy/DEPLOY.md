# Deploying the new production service (alongside the old pilot)

The old pilot stays untouched: root binary, port **8080**, `https://go.convoi.ai/invygo/search`.
The new service: `production/` binary, port **8090**, `https://go.convoi.ai/invygo-v2/search`.

All commands run from `~/convoi_ai/invygo-go-service` on the staging box.

## 1. Build the new binary (separate from the pilot)
```bash
go build -o invygo-prod ./production
```

## 2. Smoke-test it manually first
```bash
set -a; source .env; set +a            # loads INVYGO_API_KEY
PORT=8090 ./invygo-prod                 # expect: "Inventory Loaded." then "Service running on :8090"
```
In another SSH session:
```bash
source .env
curl -s -X POST http://localhost:8090/search \
  -H "x-api-key: $INVYGO_API_KEY" -H "Content-Type: application/json" \
  -d '{"city":"Riyadh","product":"STO","query":"Toyota"}'
```
Then Ctrl+C the foreground process.

## 3. Install as its own systemd service (NOT the pilot's `invygo` unit)
```bash
sudo cp production/deploy/invygo-prod.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now invygo-prod
sudo systemctl status invygo-prod --no-pager
```

## 4. Add the nginx route
Open the go.convoi.ai server block (the one that already has `location /invygo/`) and paste the
contents of `production/deploy/nginx-invygo-v2.conf` next to it, then:
```bash
sudo nginx -t && sudo systemctl reload nginx
curl -s -X POST https://go.convoi.ai/invygo-v2/search \
  -H "x-api-key: $INVYGO_API_KEY" -H "Content-Type: application/json" \
  -d '{"city":"Jeddah","product":"MONTHLY"}'
```

## Redeploy after future pulls
```bash
git pull origin
go build -o invygo-prod ./production
sudo systemctl restart invygo-prod
```
(The pilot redeploy is unchanged: build root → `sudo systemctl restart invygo`.)
