package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	_ "github.com/sijms/go-ora/v2"
)

// --- Data Models ---

type Patient struct {
	PatientID      int    `json:"patient_id"`
	Name           string `json:"name"`
	Phone          string `json:"phone"`
	NationalID     string `json:"national_id,omitempty"`
	MRN            string `json:"mrn,omitempty"`
	Gender         string `json:"gender,omitempty"`
	DOB            string `json:"dob,omitempty"`
	LastVisit      string `json:"last_visit,omitempty"`
	MedicalHistory string `json:"medical_history,omitempty"`
}

type Department struct {
	DepartmentID int    `json:"department_id"`
	Name         string `json:"name"`
	Branch       string `json:"branch"`
}

type Doctor struct {
	DoctorID     int    `json:"doctor_id"`
	Name         string `json:"name"`
	Specialty    string `json:"specialty"`
	DepartmentID int    `json:"department_id"`
	Department   string `json:"department"`
	Branch       string `json:"branch"`
}

type Slot struct {
	AvailabilityID int    `json:"availability_id"`
	DoctorID       int    `json:"doctor_id"`
	DoctorName     string `json:"doctor_name"`
	StartTime      string `json:"start_time"`
	EndTime        string `json:"end_time"`
}

type AppointmentInfo struct {
	AppointmentID   int    `json:"appointment_id"`
	BookingNumber   string `json:"booking_number"`
	PatientID       int    `json:"patient_id"`
	PatientName     string `json:"patient_name"`
	Phone           string `json:"phone"`
	DoctorName      string `json:"doctor_name"`
	Department      string `json:"department"`
	Branch          string `json:"branch"`
	AppointmentDate string `json:"appointment_date"`
	Reason          string `json:"reason"`
	Status          string `json:"status"`
}

type BookAppointmentRequest struct {
	AvailabilityID int    `json:"availability_id"`
	PatientID      int    `json:"patient_id"`   // optional if patient_name+phone given
	PatientName    string `json:"patient_name"` // used to auto-create a new patient
	Phone          string `json:"phone"`
	NationalID     string `json:"national_id"` // optional, stored on new-patient registration
	DOB            string `json:"dob"`         // optional, YYYY-MM-DD
	Gender         string `json:"gender"`      // optional
	Reason         string `json:"reason"`
}

// YaqeenIdentity is a mock of the Saudi Yaqeen identity-verification response.
type YaqeenIdentity struct {
	NationalID  string `json:"national_id"`
	FullName    string `json:"full_name"`
	Gender      string `json:"gender"`
	Nationality string `json:"nationality"`
	DOB         string `json:"dob"`
}

// Mock Yaqeen registry: identities that exist in the "government system" but
// are not yet patients — used to demo new-patient verified registration.
var yaqeenRegistry = map[string]YaqeenIdentity{
	"1055667788": {NationalID: "1055667788", FullName: "Khalid Al Otaibi", Gender: "Male", Nationality: "Saudi Arabia", DOB: "1992-03-14"},
	"1044556677": {NationalID: "1044556677", FullName: "Noura Al Qahtani", Gender: "Female", Nationality: "Saudi Arabia", DOB: "1995-07-30"},
	"2033445566": {NationalID: "2033445566", FullName: "Omar Farooq", Gender: "Male", Nationality: "Pakistan", DOB: "1988-11-02"},
}

type RescheduleRequest struct {
	AppointmentID     int `json:"appointment_id"`
	NewAvailabilityID int `json:"new_availability_id"`
}

type CancelRequest struct {
	AppointmentID int `json:"appointment_id"`
}

const timeLayout = "2006-01-02T15:04:05"

// --- Global DB Connection ---
var db *sql.DB

func initDB() {
	dbUser := os.Getenv("DB_USER")
	if dbUser == "" {
		dbUser = "hospital_user"
	}
	dbPass := os.Getenv("DB_PASS")
	if dbPass == "" {
		dbPass = "hospital_password"
	}
	dbHost := os.Getenv("DB_HOST")
	if dbHost == "" {
		dbHost = "localhost" // for local dev. Inside docker, use 'oracle-db'
	}
	dbPort := "1521"
	dbService := "FREEPDB1" // standard for gvenzl/oracle-free

	dsn := fmt.Sprintf("oracle://%s:%s@%s:%s/%s", dbUser, dbPass, dbHost, dbPort, dbService)

	var err error
	db, err = sql.Open("oracle", dsn)
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}

	for i := 0; i < 5; i++ {
		err = db.Ping()
		if err == nil {
			log.Println("Successfully connected to Oracle Database.")
			return
		}
		log.Printf("Failed to ping DB (attempt %d/5): %v", i+1, err)
		time.Sleep(5 * time.Second)
	}
	log.Fatalf("Could not connect to Oracle DB after retries: %v", err)
}

// --- Helpers ---

var nonDigits = regexp.MustCompile(`[^0-9]`)

// phoneKey normalizes a phone number for matching: digits only, last 9 digits.
// This makes '+971501234567', '971501234567' and '0501234567' all match.
func phoneKey(phone string) string {
	digits := nonDigits.ReplaceAllString(phone, "")
	if len(digits) > 9 {
		return digits[len(digits)-9:]
	}
	return digits
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{
		"status":  "error",
		"message": message,
	})
}

func fmtDate(t sql.NullTime, layout string) string {
	if !t.Valid {
		return ""
	}
	return t.Time.Format(layout)
}

// --- Handlers ---

// GetPatientsHandler fetches patients. Optionally filters by phone (digit-normalized).
// GET /patients?phone=971501234567
func GetPatientsHandler(w http.ResponseWriter, r *http.Request) {
	phone := r.URL.Query().Get("phone")

	var rows *sql.Rows
	var err error

	if phone != "" {
		query := `SELECT patient_id, name, phone, national_id, mrn, gender, dob, last_visit, medical_history
		          FROM patients
		          WHERE SUBSTR(REGEXP_REPLACE(phone, '[^0-9]', ''), -LENGTH(:1)) = :1`
		rows, err = db.Query(query, phoneKey(phone))
	} else {
		query := `SELECT patient_id, name, phone, national_id, mrn, gender, dob, last_visit, medical_history FROM patients`
		rows, err = db.Query(query)
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database query error: %v", err))
		return
	}
	defer rows.Close()

	patients := []Patient{}
	for rows.Next() {
		var p Patient
		var dob, lastVisit sql.NullTime
		var nationalID, mrn, gender, history sql.NullString
		if err := rows.Scan(&p.PatientID, &p.Name, &p.Phone, &nationalID, &mrn, &gender, &dob, &lastVisit, &history); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Error scanning row: %v", err))
			return
		}
		p.NationalID = nationalID.String
		p.MRN = mrn.String
		p.Gender = gender.String
		p.DOB = fmtDate(dob, "2006-01-02")
		p.LastVisit = fmtDate(lastVisit, "2006-01-02")
		p.MedicalHistory = history.String
		patients = append(patients, p)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"results": patients,
	})
}

// GetDepartmentsHandler returns all departments and their branches.
// GET /departments
func GetDepartmentsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT department_id, name, branch FROM departments ORDER BY department_id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database query error: %v", err))
		return
	}
	defer rows.Close()

	departments := []Department{}
	for rows.Next() {
		var d Department
		if err := rows.Scan(&d.DepartmentID, &d.Name, &d.Branch); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Error scanning row: %v", err))
			return
		}
		departments = append(departments, d)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"results": departments,
	})
}

// GetDoctorsHandler returns the doctor roster, optionally filtered.
// GET /doctors?department_id=1  |  /doctors?branch=Abu Dhabi  |  /doctors?department=ENT Care
func GetDoctorsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	query := `SELECT doc.doctor_id, doc.name, doc.specialty, dep.department_id, dep.name, dep.branch
	          FROM doctors doc JOIN departments dep ON doc.department_id = dep.department_id
	          WHERE 1=1`
	args := []interface{}{}
	i := 1

	if v := q.Get("department_id"); v != "" {
		query += fmt.Sprintf(" AND dep.department_id = :%d", i)
		args = append(args, v)
		i++
	}
	if v := q.Get("branch"); v != "" {
		query += fmt.Sprintf(" AND LOWER(dep.branch) = LOWER(:%d)", i)
		args = append(args, v)
		i++
	}
	if v := q.Get("department"); v != "" {
		query += fmt.Sprintf(" AND LOWER(dep.name) = LOWER(:%d)", i)
		args = append(args, v)
		i++
	}
	query += " ORDER BY dep.department_id, doc.name"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database query error: %v", err))
		return
	}
	defer rows.Close()

	doctors := []Doctor{}
	for rows.Next() {
		var d Doctor
		var specialty sql.NullString
		if err := rows.Scan(&d.DoctorID, &d.Name, &specialty, &d.DepartmentID, &d.Department, &d.Branch); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Error scanning row: %v", err))
			return
		}
		d.Name = stripDr(d.Name)
		d.Specialty = specialty.String
		doctors = append(doctors, d)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"results": doctors,
	})
}

// GetAvailabilityHandler returns FREE slots for a date, for a doctor or a whole department.
// GET /availability?date=2026-07-22&doctor_id=3
// GET /availability?date=2026-07-22&department_id=1
func GetAvailabilityHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	dateStr := q.Get("date")
	if dateStr == "" {
		writeError(w, http.StatusBadRequest, "Missing required query parameter: date (YYYY-MM-DD)")
		return
	}
	day, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid date format. Use YYYY-MM-DD (e.g. 2026-07-22)")
		return
	}

	doctorID := q.Get("doctor_id")
	departmentID := q.Get("department_id")
	if doctorID == "" && departmentID == "" {
		writeError(w, http.StatusBadRequest, "Provide doctor_id or department_id")
		return
	}

	query := `SELECT a.availability_id, a.doctor_id, doc.name, a.start_time, a.end_time
	          FROM doctor_availability a
	          JOIN doctors doc ON doc.doctor_id = a.doctor_id
	          WHERE a.is_booked = 0 AND a.start_time >= :1 AND a.start_time < :2`
	args := []interface{}{day, day.AddDate(0, 0, 1)}
	if doctorID != "" {
		query += " AND a.doctor_id = :3"
		args = append(args, doctorID)
	} else {
		query += " AND doc.department_id = :3"
		args = append(args, departmentID)
	}
	query += " ORDER BY a.start_time, doc.name"

	rows, err := db.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database query error: %v", err))
		return
	}
	defer rows.Close()

	slots := []Slot{}
	for rows.Next() {
		var s Slot
		var start, end time.Time
		if err := rows.Scan(&s.AvailabilityID, &s.DoctorID, &s.DoctorName, &start, &end); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Error scanning row: %v", err))
			return
		}
		s.DoctorName = stripDr(s.DoctorName)
		s.StartTime = start.Format(timeLayout)
		s.EndTime = end.Format(timeLayout)
		slots = append(slots, s)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"date":    dateStr,
		"results": slots,
	})
}

// resolvePatient finds a patient by id or normalized phone, creating one if
// needed (with an auto-generated MRN, like the HMIS "Fast Registration" flow).
// Returns (patient_id, name, mrn).
func resolvePatient(tx *sql.Tx, req BookAppointmentRequest) (int, string, string, error) {
	if req.PatientID > 0 {
		var foundName string
		var mrn sql.NullString
		err := tx.QueryRow(`SELECT name, mrn FROM patients WHERE patient_id = :1`, req.PatientID).Scan(&foundName, &mrn)
		if err == sql.ErrNoRows {
			return 0, "", "", fmt.Errorf("no patient with patient_id %d", req.PatientID)
		}
		return req.PatientID, foundName, mrn.String, err
	}

	if req.Phone == "" {
		return 0, "", "", fmt.Errorf("provide patient_id, or phone (with patient_name for new patients)")
	}

	key := phoneKey(req.Phone)
	var id int
	var foundName string
	var mrn sql.NullString

	if req.PatientName != "" {
		// A name was supplied: match phone AND name, so a corrected name or a
		// family member on a shared phone gets their own record instead of
		// silently reusing whoever registered the number first.
		err := tx.QueryRow(
			`SELECT patient_id, name, mrn FROM patients
			 WHERE SUBSTR(REGEXP_REPLACE(phone, '[^0-9]', ''), -LENGTH(:1)) = :2
			   AND UPPER(name) = UPPER(:3)
			 ORDER BY patient_id DESC FETCH FIRST 1 ROWS ONLY`, key, key, req.PatientName).Scan(&id, &foundName, &mrn)
		if err == nil {
			return id, foundName, mrn.String, nil
		}
		if err != sql.ErrNoRows {
			return 0, "", "", err
		}
		// No record under this phone+name — fall through and register them.
	} else {
		err := tx.QueryRow(
			`SELECT patient_id, name, mrn FROM patients
			 WHERE SUBSTR(REGEXP_REPLACE(phone, '[^0-9]', ''), -LENGTH(:1)) = :1
			 ORDER BY patient_id DESC FETCH FIRST 1 ROWS ONLY`, key).Scan(&id, &foundName, &mrn)
		if err == nil {
			return id, foundName, mrn.String, nil
		}
		if err != sql.ErrNoRows {
			return 0, "", "", err
		}
		// New patient — need a name to register them.
		return 0, "", "", fmt.Errorf("no patient found for this phone; provide patient_name to register them")
	}
	// Build the INSERT from the provided fields only — go-ora rejects untyped
	// nil binds (ORA-01008), so omitted columns are simply left to default NULL.
	cols := "name, phone"
	placeholders := ":1, :2"
	args := []interface{}{req.PatientName, req.Phone}
	if req.NationalID != "" {
		args = append(args, req.NationalID)
		cols += ", national_id"
		placeholders += fmt.Sprintf(", :%d", len(args))
	}
	if req.Gender != "" {
		args = append(args, req.Gender)
		cols += ", gender"
		placeholders += fmt.Sprintf(", :%d", len(args))
	}
	if req.DOB != "" {
		if d, derr := time.Parse("2006-01-02", req.DOB); derr == nil {
			args = append(args, d)
			cols += ", dob"
			placeholders += fmt.Sprintf(", :%d", len(args))
		}
	}
	if _, err := tx.Exec(
		fmt.Sprintf(`INSERT INTO patients (%s) VALUES (%s)`, cols, placeholders),
		args...); err != nil {
		return 0, "", "", fmt.Errorf("failed to create patient: %v", err)
	}
	if err := tx.QueryRow(
		`SELECT patient_id, name FROM patients
		 WHERE SUBSTR(REGEXP_REPLACE(phone, '[^0-9]', ''), -LENGTH(:1)) = :2
		   AND UPPER(name) = UPPER(:3)
		 ORDER BY patient_id DESC FETCH FIRST 1 ROWS ONLY`, key, key, req.PatientName).Scan(&id, &foundName); err != nil {
		return 0, "", "", fmt.Errorf("failed to read back new patient: %v", err)
	}
	// Generate the medical record number from the new patient id.
	newMRN := fmt.Sprintf("MRN-%06d", id)
	if _, err := tx.Exec(`UPDATE patients SET mrn = :1 WHERE patient_id = :2`, newMRN, id); err != nil {
		return 0, "", "", fmt.Errorf("failed to assign MRN: %v", err)
	}
	return id, foundName, newMRN, nil
}

// stripDr removes the "Dr. " prefix from a doctor name before it goes out in
// an API response. The voice agent's English TTS reads the abbreviation as
// "drive", so the agent gets the bare name and says "Doctor <name>" itself.
func stripDr(name string) string {
	for _, p := range []string{"Dr. ", "Dr.", "Dr "} {
		if len(name) > len(p) && name[:len(p)] == p {
			return name[len(p):]
		}
	}
	return name
}

// BookAppointmentHandler books a free availability slot.
// POST /appointments {availability_id, patient_id?, patient_name?, phone?, reason}
func BookAppointmentHandler(w http.ResponseWriter, r *http.Request) {
	var req BookAppointmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.AvailabilityID <= 0 {
		writeError(w, http.StatusBadRequest, "availability_id is required (pick one from check_availability)")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to start transaction: %v", err))
		return
	}
	defer tx.Rollback()

	// Lock the slot and verify it is still free.
	var doctorID int
	var start, end time.Time
	var doctorName, deptName, branch string
	err = tx.QueryRow(
		`SELECT a.doctor_id, a.start_time, a.end_time, doc.name, dep.name, dep.branch
		 FROM doctor_availability a
		 JOIN doctors doc ON doc.doctor_id = a.doctor_id
		 JOIN departments dep ON dep.department_id = doc.department_id
		 WHERE a.availability_id = :1 AND a.is_booked = 0
		 FOR UPDATE OF a.is_booked`, req.AvailabilityID).
		Scan(&doctorID, &start, &end, &doctorName, &deptName, &branch)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "That slot is no longer available. Re-check availability and pick another slot.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database error: %v", err))
		return
	}

	patientID, patientName, patientMRN, err := resolvePatient(tx, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := tx.Exec(`UPDATE doctor_availability SET is_booked = 1 WHERE availability_id = :1`, req.AvailabilityID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to reserve slot: %v", err))
		return
	}

	if _, err := tx.Exec(
		`INSERT INTO appointments (patient_id, doctor_id, availability_id, appointment_date, reason, status)
		 VALUES (:1, :2, :3, :4, :5, 'SCHEDULED')`,
		patientID, doctorID, req.AvailabilityID, start, req.Reason); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to insert appointment: %v", err))
		return
	}

	var appointmentID int
	if err := tx.QueryRow(
		`SELECT appointment_id FROM appointments WHERE availability_id = :1`, req.AvailabilityID).Scan(&appointmentID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to read back appointment: %v", err))
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to commit booking: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "success",
		"message":        "Appointment booked successfully.",
		"appointment_id": appointmentID,
		"booking_number": fmt.Sprintf("BK-%d", 1000+appointmentID),
		"patient_id":     patientID,
		"patient_name":   patientName,
		"mrn":            patientMRN,
		"doctor_name":    stripDr(doctorName),
		"department":     deptName,
		"branch":         branch,
		"start_time":     start.Format(timeLayout),
		"end_time":       end.Format(timeLayout),
	})
}

// YaqeenVerifyHandler is a MOCK of the Saudi Yaqeen identity-verification API.
// GET /yaqeen?national_id=1055667788&dob=1992-03-14
// Verified only when the national_id exists AND the DOB matches.
func YaqeenVerifyHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nationalID := nonDigits.ReplaceAllString(q.Get("national_id"), "")
	dob := q.Get("dob")
	if nationalID == "" {
		writeError(w, http.StatusBadRequest, "Missing required query parameter: national_id")
		return
	}

	identity, ok := yaqeenRegistry[nationalID]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":   "success",
			"verified": false,
			"message":  "No record found for this National ID / Iqama. Proceed with fast registration (full name + mobile number).",
		})
		return
	}
	if dob != "" && dob != identity.DOB {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":   "success",
			"verified": false,
			"message":  "Date of birth does not match this National ID / Iqama. Re-confirm the date of birth with the caller.",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "success",
		"verified":    true,
		"national_id": identity.NationalID,
		"full_name":   identity.FullName,
		"gender":      identity.Gender,
		"nationality": identity.Nationality,
		"dob":         identity.DOB,
	})
}

// ListAppointmentsHandler finds SCHEDULED appointments by patient phone.
// GET /appointments?phone=971501234567
func ListAppointmentsHandler(w http.ResponseWriter, r *http.Request) {
	phone := r.URL.Query().Get("phone")
	if phone == "" {
		writeError(w, http.StatusBadRequest, "Missing required query parameter: phone")
		return
	}

	rows, err := db.Query(
		`SELECT app.appointment_id, p.patient_id, p.name, p.phone, doc.name, dep.name, dep.branch,
		        app.appointment_date, app.reason, app.status
		 FROM appointments app
		 JOIN patients p ON p.patient_id = app.patient_id
		 LEFT JOIN doctors doc ON doc.doctor_id = app.doctor_id
		 LEFT JOIN departments dep ON dep.department_id = doc.department_id
		 WHERE app.status = 'SCHEDULED'
		   AND SUBSTR(REGEXP_REPLACE(p.phone, '[^0-9]', ''), -LENGTH(:1)) = :1
		 ORDER BY app.appointment_date`, phoneKey(phone))
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database query error: %v", err))
		return
	}
	defer rows.Close()

	appointments := []AppointmentInfo{}
	for rows.Next() {
		var a AppointmentInfo
		var doctorName, deptName, branch, reason sql.NullString
		var apptDate time.Time
		if err := rows.Scan(&a.AppointmentID, &a.PatientID, &a.PatientName, &a.Phone,
			&doctorName, &deptName, &branch, &apptDate, &reason, &a.Status); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Error scanning row: %v", err))
			return
		}
		a.BookingNumber = fmt.Sprintf("BK-%d", 1000+a.AppointmentID)
		a.DoctorName = stripDr(doctorName.String)
		a.Department = deptName.String
		a.Branch = branch.String
		a.Reason = reason.String
		a.AppointmentDate = apptDate.Format(timeLayout)
		appointments = append(appointments, a)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "success",
		"results": appointments,
	})
}

// RescheduleAppointmentHandler atomically moves an appointment to a new slot.
// POST /appointments/reschedule {appointment_id, new_availability_id}
func RescheduleAppointmentHandler(w http.ResponseWriter, r *http.Request) {
	var req RescheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.AppointmentID <= 0 || req.NewAvailabilityID <= 0 {
		writeError(w, http.StatusBadRequest, "appointment_id and new_availability_id are required")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to start transaction: %v", err))
		return
	}
	defer tx.Rollback()

	var oldAvailabilityID sql.NullInt64
	var status string
	err = tx.QueryRow(
		`SELECT availability_id, status FROM appointments WHERE appointment_id = :1 FOR UPDATE`,
		req.AppointmentID).Scan(&oldAvailabilityID, &status)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, fmt.Sprintf("No appointment with appointment_id %d", req.AppointmentID))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database error: %v", err))
		return
	}
	if status != "SCHEDULED" {
		writeError(w, http.StatusConflict, fmt.Sprintf("Appointment is %s and cannot be rescheduled", status))
		return
	}

	// Lock and validate the new slot.
	var newDoctorID int
	var start, end time.Time
	var doctorName, deptName, branch string
	err = tx.QueryRow(
		`SELECT a.doctor_id, a.start_time, a.end_time, doc.name, dep.name, dep.branch
		 FROM doctor_availability a
		 JOIN doctors doc ON doc.doctor_id = a.doctor_id
		 JOIN departments dep ON dep.department_id = doc.department_id
		 WHERE a.availability_id = :1 AND a.is_booked = 0
		 FOR UPDATE OF a.is_booked`, req.NewAvailabilityID).
		Scan(&newDoctorID, &start, &end, &doctorName, &deptName, &branch)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "That new slot is no longer available. The original appointment is unchanged.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database error: %v", err))
		return
	}

	if _, err := tx.Exec(`UPDATE doctor_availability SET is_booked = 1 WHERE availability_id = :1`, req.NewAvailabilityID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to reserve new slot: %v", err))
		return
	}
	if oldAvailabilityID.Valid {
		if _, err := tx.Exec(`UPDATE doctor_availability SET is_booked = 0 WHERE availability_id = :1`, oldAvailabilityID.Int64); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to release old slot: %v", err))
			return
		}
	}
	if _, err := tx.Exec(
		`UPDATE appointments SET doctor_id = :1, availability_id = :2, appointment_date = :3
		 WHERE appointment_id = :4`,
		newDoctorID, req.NewAvailabilityID, start, req.AppointmentID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to update appointment: %v", err))
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to commit reschedule: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "success",
		"message":        "Appointment rescheduled successfully.",
		"appointment_id": req.AppointmentID,
		"booking_number": fmt.Sprintf("BK-%d", 1000+req.AppointmentID),
		"doctor_name":    stripDr(doctorName),
		"department":     deptName,
		"branch":         branch,
		"start_time":     start.Format(timeLayout),
		"end_time":       end.Format(timeLayout),
	})
}

// CancelAppointmentHandler cancels an appointment and frees its slot.
// POST /appointments/cancel {appointment_id}
func CancelAppointmentHandler(w http.ResponseWriter, r *http.Request) {
	var req CancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.AppointmentID <= 0 {
		writeError(w, http.StatusBadRequest, "appointment_id is required")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to start transaction: %v", err))
		return
	}
	defer tx.Rollback()

	var availabilityID sql.NullInt64
	var status string
	err = tx.QueryRow(
		`SELECT availability_id, status FROM appointments WHERE appointment_id = :1 FOR UPDATE`,
		req.AppointmentID).Scan(&availabilityID, &status)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, fmt.Sprintf("No appointment with appointment_id %d", req.AppointmentID))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Database error: %v", err))
		return
	}
	if status != "SCHEDULED" {
		writeError(w, http.StatusConflict, fmt.Sprintf("Appointment is already %s", status))
		return
	}

	if _, err := tx.Exec(`UPDATE appointments SET status = 'CANCELLED' WHERE appointment_id = :1`, req.AppointmentID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to cancel appointment: %v", err))
		return
	}
	if availabilityID.Valid {
		if _, err := tx.Exec(`UPDATE doctor_availability SET is_booked = 0 WHERE availability_id = :1`, availabilityID.Int64); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to release slot: %v", err))
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to commit cancellation: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "success",
		"message":        "Appointment cancelled successfully.",
		"appointment_id": req.AppointmentID,
	})
}

func main() {
	log.Println("Starting Hospital Demo Service...")

	initDB()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /patients", GetPatientsHandler)
	mux.HandleFunc("GET /yaqeen", YaqeenVerifyHandler)
	mux.HandleFunc("GET /departments", GetDepartmentsHandler)
	mux.HandleFunc("GET /doctors", GetDoctorsHandler)
	mux.HandleFunc("GET /availability", GetAvailabilityHandler)
	mux.HandleFunc("GET /appointments", ListAppointmentsHandler)
	mux.HandleFunc("POST /appointments", BookAppointmentHandler)
	mux.HandleFunc("POST /appointments/reschedule", RescheduleAppointmentHandler)
	mux.HandleFunc("POST /appointments/cancel", CancelAppointmentHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8890"
	}

	log.Printf("Service running on :%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
