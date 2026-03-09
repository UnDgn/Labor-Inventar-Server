package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	deviceComments = make(map[string]string) // MAC -> Kommentar
	commentMutex   sync.Mutex
)

func commentFilePath() string {
	exe, err := os.Executable()
	if err != nil {
		return filepath.Join("data", "comments.json")
	}
	baseDir := filepath.Dir(exe)
	return filepath.Join(baseDir, "data", "comments.json")
}

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

func getCommentForMAC(mac string) string {
	commentMutex.Lock()
	defer commentMutex.Unlock()

	return deviceComments[normalizeMAC(mac)]
}

func setCommentForMAC(mac, comment string) {
	commentMutex.Lock()
	defer commentMutex.Unlock()

	mac = normalizeMAC(mac)
	comment = strings.TrimSpace(comment)

	if mac == "" {
		return
	}

	if comment == "" {
		delete(deviceComments, mac)
		return
	}

	deviceComments[mac] = comment
}
