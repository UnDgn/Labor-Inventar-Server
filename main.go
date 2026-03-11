package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ------------------------------------------------------------
// HTTP-Handler
// ------------------------------------------------------------

// handleTriggerScan stößt manuell einen neuen Netzwerkscan an.

// Der Trigger wird nicht blockierend in den Kanal geschrieben.
// Ist bereits ein Trigger vorhanden, wird einfach nichts weiter getan.
func handleTriggerScan(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Manueller Scan-Trigger empfangen.")

	select {
	case scanTrigger <- struct{}{}:
	default:
	}

	w.WriteHeader(http.StatusOK)
}

// handleDashboard rendert die HTML-Übersichtsseite.

// Die eigentliche Modellbildung und HTML-Ausgabe passiert in:
// - buildDashboardModel()
// - renderDashboard(...)
func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	model := buildDashboardModel()
	renderDashboard(w, model)
}

// ------------------------------------------------------------
// Office-Handler
// ------------------------------------------------------------

// officeAssignmentRequest beschreibt das JSON-Payload
// für das Setzen einer Büro-/Raumzuordnung.

// Erwartetes Format:

//	{
//	  "mac": "AA:BB:CC:DD:EE:FF",
//	  "office": "T4015"
//	}
type officeAssignmentRequest struct {
	MAC    string `json:"mac"`
	Office string `json:"office"`
}

// isValidOffice prüft, ob ein Office-Wert in der zentralen Liste validOffices enthalten ist.

// Leerer String ist erlaubt und bedeutet:
// vorhandene Zuordnung entfernen.
func isValidOffice(office string) bool {
	if office == "" {
		return true
	}

	for _, valid := range validOffices {
		if office == valid {
			return true
		}
	}

	return false
}

// handleOfficeAssignment verarbeitet Office-Zuordnungen aus dem Frontend.
//
// Ablauf:
// 1. nur POST erlauben
// 2. JSON lesen
// 3. MAC normalisieren
// 4. Office validieren
// 5. Zuordnung persistent speichern
// 6. laufendes Inventory sofort aktualisieren
func handleOfficeAssignment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req officeAssignmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.MAC = normalizeMAC(req.MAC)

	if req.MAC == "" {
		http.Error(w, "missing mac", http.StatusBadRequest)
		return
	}

	if !isValidOffice(req.Office) {
		http.Error(w, "invalid office", http.StatusBadRequest)
		return
	}

	// Zuordnung persistent setzen
	setOfficeForMAC(req.MAC, req.Office)

	if err := saveOfficeAssignments(); err != nil {
		http.Error(w, "failed to save office assignment", http.StatusInternalServerError)
		return
	}

	// Bereits geladene Geräte im RAM sofort aktualisieren,
	// damit das Dashboard ohne Neustart den neuen Wert zeigt.
	inventoryMutex.Lock()
	for _, dev := range inventory {
		if normalizeMAC(dev.MACAddress) == req.MAC {
			dev.Office = req.Office
		}
	}
	inventoryMutex.Unlock()

	w.WriteHeader(http.StatusOK)
}

// ------------------------------------------------------------
// Comment-Handler
// ------------------------------------------------------------

// commentRequest beschreibt das JSON-Payload
// für das Setzen eines Kommentars pro MAC-Adresse.

// Erwartetes Format:

//	{
//	  "mac": "AA:BB:CC:DD:EE:FF",
//	  "comment": "Gerät steht im Schaltschrank links"
//	}
type commentRequest struct {
	MAC     string `json:"mac"`
	Comment string `json:"comment"`
}

// handleCommentAssignment verarbeitet Kommentar-Änderungen aus dem Frontend.

// Ablauf:
// 1. nur POST erlauben
// 2. JSON lesen
// 3. MAC normalisieren
// 4. Kommentar persistent speichern
// 5. laufendes Inventory sofort aktualisieren
func handleCommentAssignment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req commentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	req.MAC = normalizeMAC(req.MAC)

	if req.MAC == "" {
		http.Error(w, "missing mac", http.StatusBadRequest)
		return
	}

	setCommentForMAC(req.MAC, req.Comment)

	if err := saveComments(); err != nil {
		http.Error(w, "failed to save comment", http.StatusInternalServerError)
		return
	}

	// Bereits geladene Geräte im RAM sofort aktualisieren,
	// damit das Dashboard direkt den neuen Kommentar anzeigt.
	inventoryMutex.Lock()
	for _, dev := range inventory {
		if normalizeMAC(dev.MACAddress) == req.MAC {
			dev.Comment = req.Comment
		}
	}
	inventoryMutex.Unlock()

	w.WriteHeader(http.StatusOK)
}

// ------------------------------------------------------------
// main
// ------------------------------------------------------------

// main ist der Einstiegspunkt des Programms.

// Reihenfolge:
// 1. Snapshot laden
// 2. Hintergrund-Discovery starten
// 3. HTTP-Routen registrieren
// 4. Zusatzdaten (Office / Comments) laden
// 5. Webserver starten
func main() {
	// Vorhandenen Snapshot laden, damit bekannte Geräte und Zustände
	// beim Start sofort wieder verfügbar sind.
	if err := loadSnapshot(); err != nil {
		fmt.Println("Snapshot load error:", err)
	}

	// Hintergrund-Scan starten
	go runDiscovery()

	// Statische Dateien und HTTP-Endpunkte registrieren
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/trigger-scan", handleTriggerScan)
	http.HandleFunc("/api/office", handleOfficeAssignment)
	http.HandleFunc("/api/comment", handleCommentAssignment)

	fmt.Println("-----------------------------------------------")
	const port = 18080

	fmt.Println("Beckhoff Inventar-Server Online")
	fmt.Printf("Lokal: http://localhost:%d\n", port)

	// Lokale Testnetz-IP ermitteln, damit in der Konsole direkt
	// die Netzwerk-URL angezeigt werden kann.
	if ip, _, err := getLocalLabIPv4(); err == nil {
		fmt.Printf("Netz:  http://%s:%d\n", ip.String(), port)
	} else {
		fmt.Println("Netz:  (keine 172.17.76.x Adresse gefunden)")
	}

	fmt.Println("-----------------------------------------------")

	// Persistente Office-Zuordnungen laden
	if err := loadOfficeAssignments(); err != nil {
		fmt.Println("Office assignments load error:", err)
	} else {
		fmt.Println("Office assignments loaded")
	}

	// Persistente Kommentare laden
	if err := loadComments(); err != nil {
		fmt.Println("Comments load error:", err)
	} else {
		fmt.Println("Comments loaded")
	}

	// HTTP-Server starten
	if err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), nil); err != nil {
		fmt.Println("ListenAndServe error:", err)
	}
}
