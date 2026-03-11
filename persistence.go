package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// snapshotEnvelope kapselt das Inventory zusammen mit Metadaten.
// Dadurch kann neben den Gerätedaten auch gespeichert werden,
// wann der Snapshot erzeugt wurde.
type snapshotEnvelope struct {
	SavedAt   time.Time       `json:"saved_at"`  // Zeitpunkt des Speicherns
	Inventory map[string]*IPC `json:"inventory"` // kompletter Gerätestand
}

// ------------------------------------------------------------
// Snapshot-Dateipfad bestimmen
// ------------------------------------------------------------

// snapshotPath liefert immer einen absoluten Pfad relativ zur EXE:
//
// <exe-dir>/data/inventory_snapshot.json
//
// Vorteil:
// Das Tool funktioniert unabhängig davon,
// aus welchem Arbeitsverzeichnis es gestartet wird.
func snapshotPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}

	base := filepath.Dir(exe)

	return filepath.Join(base, "data", "inventory_snapshot.json"), nil
}

// ------------------------------------------------------------
// Snapshot laden
// ------------------------------------------------------------

// loadSnapshot lädt beim Start den zuletzt gespeicherten Gerätestand
// aus der JSON-Datei in das RAM-Inventory.

// Dadurch bleiben bekannte Geräte erhalten,
// auch wenn sie beim nächsten Start zunächst offline sind.
func loadSnapshot() error {
	path, err := snapshotPath()
	if err != nil {
		return err
	}

	// Datei lesen
	b, err := os.ReadFile(path)
	if err != nil {
		// Kein Snapshot vorhanden = normaler Erststart
		if os.IsNotExist(err) {
			fmt.Println("Snapshot: keiner vorhanden (Start ohne Persistenzdaten).")
			return nil
		}

		return fmt.Errorf("read snapshot: %w", err)
	}

	// JSON dekodieren
	var env snapshotEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	// Daten unter Lock ins globale Inventory übernehmen
	inventoryMutex.Lock()

	if inventory == nil {
		inventory = make(map[string]*IPC)
	}

	for ip, dev := range env.Inventory {
		inventory[ip] = dev
	}

	inventoryMutex.Unlock()

	fmt.Printf(
		"Snapshot geladen: %s (%d Geräte)\n",
		env.SavedAt.Local().Format("02.01.2006 15:04:05"),
		len(env.Inventory),
	)

	return nil
}

// ------------------------------------------------------------
// Snapshot speichern
// ------------------------------------------------------------

// saveSnapshot schreibt den aktuellen Gerätestand in JSON.

// Wichtig:
// Das Schreiben erfolgt atomar:

// 1. temp-Datei schreiben
// 2. temp-Datei umbenennen

// Dadurch bleibt die Snapshot-Datei konsistent,
// selbst wenn während des Schreibens ein Fehler auftritt.
func saveSnapshot() error {
	path, err := snapshotPath()
	if err != nil {
		return err
	}

	// --------------------------------------------------------
	// Inventory unter Lock kopieren
	// --------------------------------------------------------

	// Warum kopieren?

	// Während JSON geschrieben wird, soll der Lock nicht gehalten werden,
	// damit Scanner und Webserver weiterarbeiten können.
	//
	// Deshalb wird zuerst eine vollständige Kopie erzeugt.
	inventoryMutex.Lock()

	copyMap := make(map[string]*IPC, len(inventory))

	for ip, dev := range inventory {
		if dev == nil {
			continue
		}

		// Strukturwert kopieren, damit keine Pointer direkt ins Live-Inventory zeigen
		d := *dev
		copyMap[ip] = &d
	}

	inventoryMutex.Unlock()

	// --------------------------------------------------------
	// Snapshot-Objekt bauen
	// --------------------------------------------------------

	env := snapshotEnvelope{
		SavedAt:   time.Now(),
		Inventory: copyMap,
	}

	// --------------------------------------------------------
	// Zielverzeichnis sicherstellen
	// --------------------------------------------------------

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}

	// --------------------------------------------------------
	// JSON formatieren
	// --------------------------------------------------------

	// MarshalIndent erzeugt bewusst lesbares JSON,
	// damit Snapshot-Datei auch manuell geprüft werden kann.
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	// --------------------------------------------------------
	// Atomisches Schreiben
	// --------------------------------------------------------

	// Erst temp-Datei schreiben,
	// danach per Rename ersetzen.

	// Vorteil:
	// Niemals halbfertige Snapshot-Datei im Fehlerfall.
	tmp := path + ".tmp"

	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp snapshot: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}

	fmt.Println(
		"Snapshot gespeichert:",
		env.SavedAt.Local().Format("02.01.2006 15:04:05"),
	)

	return nil
}
