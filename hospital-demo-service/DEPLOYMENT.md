# Hospital Demo Service — EC2 Deployment Runbook

Standalone Go API + Oracle DB that backs the Magrabi Health voice-agent demo.
The voice agent calls this service over HTTP for doctors, availability, patients
and appointments.

**Important:** this is a **separate Go module** inside the `invygo-go-service`
repo. The existing `.github/workflows/deploy.yml` builds and restarts ONLY the
root `invygo-service` — it does **not** touch this service. Set this up once by
hand; afterwards, updates need a manual rebuild + restart (see §7).

---

## 1. Prerequisites

| Requirement | Why |
|---|---|
| Docker + Docker Compose plugin | Runs the Oracle DB container |
| **~5 GB free RAM** | `gvenzl/oracle-free` needs ~4 GB; the Go binary is negligible |
| ~10 GB free disk | Oracle image + datafiles |
| Go 1.25+ (`/usr/local/go/bin/go` already on this box) | Builds the service binary |

Check capacity first — if the instance is small, this is the step that fails:

```bash
free -h          # need ~5 GB available
df -h /          # need ~10 GB free
docker --version && docker compose version
```

---

## 2. Code location

Already on the box after the last CI deploy:

```bash
cd ~/convoi_ai/invygo-go-service/hospital-demo-service
ls    # main.go, init.sql, docker-compose.yml, go.mod, agent_tools_config.json
```

If missing: `cd ~/convoi_ai/invygo-go-service && git pull origin feature/invygo-trial`

---

## 3. Start the Oracle database

```bash
cd ~/convoi_ai/invygo-go-service/hospital-demo-service
docker compose up -d
docker logs -f hospital-oracle-db     # wait for "DATABASE IS READY TO USE!" (first run: 5-10 min)
```

`init.sql` runs automatically on first start and creates the schema + seed data
(21 doctors, 5 branch/department pairs, 14 days of 30-minute slots, 3 patients).

### 3a. VERIFY THE SCHEMA — do not skip

The Oracle image runs init scripts as `SYS` in the root container. `init.sql`
starts with a `CONNECT` line to force the app schema; if that ever gets lost,
tables land invisible to the service and every endpoint 500s with
`ORA-00942: table or view does not exist`.

```bash
echo "SELECT table_name FROM user_tables;" | docker exec -i hospital-oracle-db \
  sqlplus -s hospital_user/hospital_password@localhost:1521/FREEPDB1
```

Expected: `PATIENTS`, `DEPARTMENTS`, `DOCTORS`, `DOCTOR_AVAILABILITY`, `APPOINTMENTS`.
"no rows selected" → the schema went to the wrong user; re-run §6 (re-seed).

---

## 4. Build the service binary

It is a separate module — `cd` into the directory first, or the root build
will not include it:

```bash
cd ~/convoi_ai/invygo-go-service/hospital-demo-service
/usr/local/go/bin/go build -o hospital-demo-service .
./hospital-demo-service &        # quick check
curl -s localhost:8890/departments
kill %1
```

---

## 5. systemd service

```bash
sudo tee /etc/systemd/system/hospital-demo.service > /dev/null <<'EOF'
[Unit]
Description=Hospital Demo Service (Magrabi voice-agent demo API)
After=docker.service network-online.target
Requires=docker.service

[Service]
Type=simple
User=ubuntu
WorkingDirectory=/home/ubuntu/convoi_ai/invygo-go-service/hospital-demo-service
ExecStart=/home/ubuntu/convoi_ai/invygo-go-service/hospital-demo-service/hospital-demo-service
Environment=DB_HOST=localhost
Environment=DB_USER=hospital_user
Environment=DB_PASS=hospital_password
Environment=PORT=8890
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now hospital-demo.service
sudo systemctl status hospital-demo.service --no-pager
journalctl -u hospital-demo.service -n 30 --no-pager
```

Adjust `User=` / paths if the repo lives under a different user than `ubuntu`.

### 5a. Network exposure — keep port 8890 PRIVATE

The API has **no authentication** and serves patient data. Do **not** open 8890
in the EC2 security group.

- Voice agent worker on the **same box** → it calls `http://localhost:8890`
  (or `http://host.docker.internal:8890` if the worker is in Docker — that needs
  `extra_hosts: ["host.docker.internal:host-gateway"]` in the worker's compose).
- Worker on a **different box** → allow 8890 inbound **only** from that box's
  security group / private IP, over the VPC.

Confirm nothing is publicly listening: `sudo ss -tlnp | grep 8890`

---

## 6. Re-seed (before each demo, or if the schema is wrong)

Slots are seeded for **14 days from the moment the DB was initialised**, so a
long-lived container eventually runs out of future availability. Re-seeding
wipes ALL demo data and regenerates a fresh 14 days:

```bash
cd ~/convoi_ai/invygo-go-service/hospital-demo-service
docker compose down -v        # -v drops the volume; init.sql only runs on a fresh volume
docker compose up -d
docker logs -f hospital-oracle-db   # wait for "DATABASE IS READY TO USE!"
# then re-run the §3a verification
sudo systemctl restart hospital-demo.service
```

---

## 7. Updating after a code change

The CI workflow does not rebuild this service. After pulling new code:

```bash
cd ~/convoi_ai/invygo-go-service && git pull origin feature/invygo-trial
cd hospital-demo-service
/usr/local/go/bin/go build -o hospital-demo-service .
sudo systemctl restart hospital-demo.service
```

A change to `init.sql` additionally requires the §6 re-seed (or a manual
`ALTER TABLE` — init scripts do not re-run on an existing volume).

---

## 8. Smoke tests (run after every deploy)

```bash
BASE=http://localhost:8890
curl -s $BASE/departments | head -c 200; echo
curl -s "$BASE/doctors?department_id=5" | head -c 200; echo
curl -s "$BASE/availability?date=$(date -d tomorrow +%F)&department_id=1" | head -c 200; echo
curl -s "$BASE/patients?phone=971501234567" | head -c 200; echo
curl -s "$BASE/yaqeen?national_id=1055667788&dob=1992-03-14"; echo
```

All five must return `"status":"success"` with non-empty `results`.
The availability call must return a non-empty list — empty means the seeded
14-day window has expired → re-seed (§6).

---

## 9. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ORA-00942: table or view does not exist` | Schema created under `SYS`, not `hospital_user` | Re-seed (§6); confirm `init.sql` line 1 is the `CONNECT` statement |
| Service exits: `Could not connect to Oracle DB after retries` | DB container not healthy yet | `docker ps` → wait for `(healthy)`; systemd `Restart=always` will recover on its own |
| `/availability` returns empty for every date | Seeded 14-day window has passed | Re-seed (§6) |
| Slot times look 4 hours off | Container not on clinic time | `TZ=Asia/Dubai` must be set in `docker-compose.yml` → recreate the container |
| Booking returns HTTP 409 | Slot already taken (correct behaviour) | Agent re-checks availability and offers another slot |
| Oracle container OOM-killed / won't start | Instance RAM below ~5 GB | Resize the instance or move the DB to a dedicated box |

---

## 10. Endpoint reference (what the voice agent calls)

| Method | Path | Purpose |
|---|---|---|
| GET | `/departments` | Branch + department list with ids |
| GET | `/doctors?department_id=&branch=` | Live doctor roster |
| GET | `/availability?date=&doctor_id=\|department_id=` | Free 30-min slots |
| GET | `/patients?phone=` | Patient lookup (digit-normalised) |
| GET | `/yaqeen?national_id=&dob=` | Mock identity verification |
| GET | `/appointments?phone=` | Scheduled appointments (for reschedule/cancel) |
| POST | `/appointments` | Book a slot (auto-registers new patients) |
| POST | `/appointments/reschedule` | Atomic move to a new slot |
| POST | `/appointments/cancel` | Cancel and free the slot |

`agent_tools_config.json` holds the matching tool definitions for the voice
platform — its `base_url` must point at wherever this service ends up.
