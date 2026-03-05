package main

import (
	"fmt"
	"net/http"
)

// --- 6. WEB HANDLER ---

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

// --- 7. MAIN ---

func main() {
	//Snapshot laden
	if err := loadSnapshot(); err != nil {
		fmt.Println("Snapshot load error:", err)
	}
	go runDiscovery()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/trigger-scan", handleTriggerScan)

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

	if err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), nil); err != nil {
		fmt.Println("ListenAndServe error:", err)
	}
}
