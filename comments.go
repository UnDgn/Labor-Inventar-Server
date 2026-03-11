package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ------------------------------------------------------------
// Kommentar-Zuordnungen pro Gerät
// ------------------------------------------------------------
//
// deviceComments speichert Kommentare MAC-basiert.
//
// Schlüssel:
//
//	normalisierte MAC-Adresse
//
// Wert:
//
//	frei editierbarer Kommentar aus dem Dashboard
//
// Beispiel:
//
//	"00:01:05:12:34:56" -> "Prüfstand 4 / aktuell ohne Lizenz"
var (
	deviceComments = make(map[string]string) // MAC -> Kommentar
	commentMutex   sync.Mutex
)

//
// ------------------------------------------------------------
// Dateipfad für comments.json bestimmen
// ------------------------------------------------------------
//

// commentFilePath liefert den absoluten Speicherort für comments.json.
//
// Ziel:
// <exe-dir>/data/comments.json
//
// Dadurch bleibt die Datei immer neben der EXE
// und funktioniert unabhängig vom aktuellen Startverzeichnis.
func commentFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join("data", "comments.json")
	}

	baseDir := filepath.Dir(exe)
	return filepath.Join(baseDir, "data", "comments.json")
}

//
// ------------------------------------------------------------
// Kommentare laden
// ------------------------------------------------------------
//

// loadComments lädt vorhandene Kommentar-Zuordnungen aus comments.json.
//
// Verhalten:
// - wenn Datei fehlt → leer starten
// - wenn Datei vorhanden → JSON einlesen
func loadComments() error {
	commentMutex.Lock()
	defer commentMutex.Unlock()

	path := commentFilePath()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			deviceComments = make(map[string]string)
			return nil
		}
		return err
	}

	var loaded map[string]string

	if err := json.Unmarshal(data, &loaded); err != nil {
		return err
	}

	if loaded == nil {
		loaded = make(map[string]string)
	}

	deviceComments = loaded

	return nil
}

//
// ------------------------------------------------------------
// Kommentare speichern
// ------------------------------------------------------------
//

// saveComments schreibt alle Kommentare atomar nach comments.json.
//
// Ablauf:
// 1. JSON erzeugen
// 2. tmp-Datei schreiben
// 3. tmp-Datei umbenennen
//
// Vorteil:
// keine beschädigte Datei bei Abbruch während des Schreibens.
func saveComments() error {
	commentMutex.Lock()
	defer commentMutex.Unlock()

	path := commentFilePath()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(deviceComments, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

//
// ------------------------------------------------------------
// Kommentar zu MAC lesen
// ------------------------------------------------------------
//

// getCommentForMAC liefert den gespeicherten Kommentar
// für eine normalisierte MAC-Adresse zurück.
func getCommentForMAC(mac string) string {
	commentMutex.Lock()
	defer commentMutex.Unlock()

	return deviceComments[normalizeMAC(mac)]
}

//
// ------------------------------------------------------------
// Kommentar setzen oder löschen
// ------------------------------------------------------------
//

// setCommentForMAC speichert einen Kommentar für ein Gerät.
//
// Regeln:
// - MAC wird normalisiert
// - Leerstring bei Kommentar = Eintrag löschen
func setCommentForMAC(mac, comment string) {
	commentMutex.Lock()
	defer commentMutex.Unlock()

	mac = normalizeMAC(mac)
	comment = strings.TrimSpace(comment)

	if mac == "" {
		return
	}

	// leerer Kommentar = löschen
	if comment == "" {
		delete(deviceComments, mac)
		return
	}

	deviceComments[mac] = comment
}
