package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

//  WEB HANDLER

func handleTriggerScan(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Manueller Scan-Trigger empfangen.")

	// Nicht blockieren, wenn schon ein Trigger ansteht
	select {
	case scanTrigger <- struct{}{}:
	default:
	}

	w.WriteHeader(http.StatusOK)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	model := buildDashboardModel()
	renderDashboard(w, model)
}

//Room-Handler

type officeAssignmentRequest struct {
	MAC    string `json:"mac"`
	Office string `json:"office"`
}

func isValidOffice(office string) bool {
	if office == "" {
		return true
	}

	switch office {
	case "T4015", "T4016", "T4017", "T4018", "T4019",
		"T4020", "T4021", "T4022", "T4023", "T4024",
		"T4025", "T4026", "T4027", "T4028", "T4029",
		"T4030", "T4031", "T4032", "T4033", "T4034",
		"T4035", "T4036", "T4037", "T4038":
		return true
	default:
		return false
	}
}

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

	setOfficeForMAC(req.MAC, req.Office)

	if err := saveOfficeAssignments(); err != nil {
		http.Error(w, "failed to save office assignment", http.StatusInternalServerError)
		return
	}

	// laufendes Inventory direkt aktualisieren
	inventoryMutex.Lock()
	for _, dev := range inventory {
		if normalizeMAC(dev.MACAddress) == req.MAC {
			dev.Office = req.Office
		}
	}
	inventoryMutex.Unlock()

	w.WriteHeader(http.StatusOK)
}

// Comments-Handler

type commentRequest struct {
	MAC     string `json:"mac"`
	Comment string `json:"comment"`
}

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

	// Laufendes Inventory direkt aktualisieren
	inventoryMutex.Lock()
	for _, dev := range inventory {
		if normalizeMAC(dev.MACAddress) == req.MAC {
			dev.Comment = req.Comment
		}
	}
	inventoryMutex.Unlock()

	w.WriteHeader(http.StatusOK)
}

func main() {
	//Snapshot laden
	if err := loadSnapshot(); err != nil {
		fmt.Println("Snapshot load error:", err)
	}
	go runDiscovery()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/trigger-scan", handleTriggerScan)
	http.HandleFunc("/api/office", handleOfficeAssignment)
	http.HandleFunc("/api/comment", handleCommentAssignment)

	fmt.Println("-----------------------------------------------")
	const port = 18080

	fmt.Println("Beckhoff Inventar-Server Online")
	fmt.Printf("Lokal: http://localhost:%d\n", port)

	if ip, _, err := getLocalLabIPv4(); err == nil {
		fmt.Printf("Netz:  http://%s:%d\n", ip.String(), port)
	} else {
		fmt.Println("Netz:  (keine 172.17.76.x Adresse gefunden)")
	}

	fmt.Println("-----------------------------------------------")

	if err := loadOfficeAssignments(); err != nil {
		fmt.Println("Office assignments load error:", err)
	} else {
		fmt.Println("Office assignments loaded")
	}

	if err := loadComments(); err != nil {
		fmt.Println("Comments load error:", err)
	} else {
		fmt.Println("Comments loaded")
	}

	if err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), nil); err != nil {
		fmt.Println("ListenAndServe error:", err)
	}
}
