package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	officeAssignments = make(map[string]string) // MAC -> Büro
	officeMutex       sync.Mutex
)

func officeFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join("data", "offices.json")
	}
	baseDir := filepath.Dir(exe)
	return filepath.Join(baseDir, "data", "offices.json")
}

func loadOfficeAssignments() error {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	path := officeFilePath()

	data, err := os.ReadFile(path)
	if err != nil {
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

	if loaded == nil {
		loaded = make(map[string]string)
	}

	officeAssignments = loaded
	return nil
}

func saveOfficeAssignments() error {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	path := officeFilePath()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(officeAssignments, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

func getOfficeForMAC(mac string) string {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	return officeAssignments[normalizeMAC(mac)]
}

func setOfficeForMAC(mac, office string) {
	officeMutex.Lock()
	defer officeMutex.Unlock()

	mac = normalizeMAC(mac)
	office = strings.TrimSpace(office)

	if mac == "" {
		return
	}

	if office == "" {
		delete(officeAssignments, mac)
		return
	}

	officeAssignments[mac] = office
}
