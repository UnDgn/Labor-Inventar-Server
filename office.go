package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	// officeAssignments speichert die Zuordnung:
	// normalisierte MAC-Adresse -> Büro / Standort / Raum
	officeAssignments = make(map[string]string)

	// officeMutex schützt den gleichzeitigen Zugriff auf officeAssignments.
	officeMutex sync.Mutex
)

// ------------------------------------------------------------
// Dateipfad für Office-Zuordnungen
// ------------------------------------------------------------

// officeFilePath liefert den Pfad zur JSON-Datei mit den Office-Zuordnungen.

// Standardpfad:
//	<exe-dir>/data/offices.json

// Falls der Pfad zur EXE nicht bestimmt werden kann,
// wird auf einen relativen Fallback-Pfad zurückgegriffen.
func officeFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join("data", "offices.json")
	}

	baseDir := filepath.Dir(exe)
	return filepath.Join(baseDir, "data", "offices.json")
}

// ------------------------------------------------------------
// Office-Zuordnungen laden
// ------------------------------------------------------------

// loadOfficeAssignments lädt die gespeicherten MAC->Office-Zuordnungen
// aus der Datei offices.json in den Arbeitsspeicher.

// Verhalten:
// - Wenn die Datei nicht existiert, wird mit leerer Map gestartet.
// - Wenn die Datei existiert, wird sie vollständig geladen.
func loadOfficeAssignments() error {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	path := officeFilePath()

	data, err := os.ReadFile(path)
	if err != nil {
		// Kein Fehler im eigentlichen Sinn:
		// Wenn die Datei noch nicht existiert, starten wir einfach leer.
		if os.IsNotExist(err) {
			officeAssignments = make(map[string]string)
			return nil
		}
		return err
	}

	var loaded map[string]string
	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	// Sicherheitshalber: nil-Map vermeiden
	if loaded == nil {
		loaded = make(map[string]string)
	}

	officeAssignments = loaded
	return nil
}

// ------------------------------------------------------------
// Office-Zuordnungen speichern
// ------------------------------------------------------------

// saveOfficeAssignments schreibt die aktuelle MAC->Office-Zuordnung
// in die Datei offices.json.

// Das Schreiben erfolgt atomar:
// - erst temp-Datei schreiben
// - dann per Rename ersetzen

// So bleibt die Datei auch bei Fehlern konsistent.
func saveOfficeAssignments() error {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	path := officeFilePath()

	// Zielverzeichnis sicherstellen
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	// JSON lesbar formatieren
	data, err := json.MarshalIndent(officeAssignments, "", "  ")
	if err != nil {
		return err
	}

	// Erst temporär schreiben, dann atomar ersetzen
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

// ------------------------------------------------------------
// Office-Zuordnung lesen
// ------------------------------------------------------------

// getOfficeForMAC liefert das zugeordnete Office zu einer MAC-Adresse.

// Vor dem Lookup wird die MAC normalisiert,
// damit unterschiedliche Schreibweisen gleich behandelt werden.

// Beispiel:
//
//	aa-bb-cc-dd-ee-ff
//	AA:BB:CC:DD:EE:FF
//
// führen beide auf denselben Key.
func getOfficeForMAC(mac string) string {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	return officeAssignments[normalizeMAC(mac)]
}

//
// ------------------------------------------------------------
// Office-Zuordnung setzen / löschen
// ------------------------------------------------------------
//

// setOfficeForMAC setzt oder löscht die Office-Zuordnung für eine MAC-Adresse.

// Regeln:
// - leere MAC -> ignorieren
// - leeres Office -> Zuordnung löschen
// - sonst Office unter normalisierter MAC speichern
func setOfficeForMAC(mac, office string) {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	mac = normalizeMAC(mac)
	office = strings.TrimSpace(office)

	// Ohne gültige MAC keine Aktion
	if mac == "" {
		return
	}

	// Leeres Office bedeutet: vorhandene Zuordnung entfernen
	if office == "" {
		delete(officeAssignments, mac)
		return
	}

	officeAssignments[mac] = office
}
