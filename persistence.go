package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// snapshotEnvelope erlaubt Metadaten + Inventory in einem JSON
type snapshotEnvelope struct {
	SavedAt   time.Time       `json:"saved_at"`
	Inventory map[string]*IPC `json:"inventory"`
}

// snapshotPath returns an absolute path next to the executable:
// <exe-dir>/data/inventory_snapshot.json
func snapshotPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	base := filepath.Dir(exe)
	return filepath.Join(base, "data", "inventory_snapshot.json"), nil
}

// loadSnapshot loads the last snapshot into the in-memory inventory.
// If the file doesn't exist, it's not an error.
func loadSnapshot() error {
	path, err := snapshotPath()
	if err != nil {
		return err
	}

	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("Snapshot: keiner vorhanden (Start ohne Persistenzdaten).")
			return nil
		}
		return fmt.Errorf("read snapshot: %w", err)
	}

	var env snapshotEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	inventoryMutex.Lock()
	if inventory == nil {
		inventory = make(map[string]*IPC)
	}
	for ip, dev := range env.Inventory {
		inventory[ip] = dev
	}
	inventoryMutex.Unlock()

	fmt.Printf("Snapshot geladen: %s (%d Geräte)\n",
		env.SavedAt.Local().Format("02.01.2006 15:04:05"),
		len(env.Inventory),
	)
	return nil
}

// saveSnapshot writes the current inventory as JSON snapshot (atomic write).
func saveSnapshot() error {
	path, err := snapshotPath()
	if err != nil {
		return err
	}

	// Copy inventory under lock (so we don't hold the lock while encoding/writing)
	inventoryMutex.Lock()
	copyMap := make(map[string]*IPC, len(inventory))
	for ip, dev := range inventory {
		if dev == nil {
			continue
		}
		d := *dev // copy struct value
		copyMap[ip] = &d
	}
	inventoryMutex.Unlock()

	env := snapshotEnvelope{
		SavedAt:   time.Now(),
		Inventory: copyMap,
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	// Atomic write: write temp file then rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename snapshot: %w", err)
	}

	fmt.Println("Snapshot gespeichert:", env.SavedAt.Local().Format("02.01.2006 15:04:05"))
	return nil
}
