package main

import (
	"fmt"
	"strings"
)

// normalizeMAC normalisiert MAC-Adressen auf "AA:BB:CC:DD:EE:FF"
func normalizeMAC(mac string) string {
	mac = strings.TrimSpace(mac)
	mac = strings.ToUpper(mac)
	mac = strings.ReplaceAll(mac, "-", ":")
	return mac
}

// findIPByMACLocked sucht einen vorhandenen Inventory-Eintrag mit gleicher MAC.
// MUSS unter inventoryMutex.Lock() aufgerufen werden!
func findIPByMACLocked(mac, exceptIP string) (string, *IPC, bool) {
	if mac == "" {
		return "", nil, false
	}
	for ip, dev := range inventory {
		if dev == nil || ip == exceptIP {
			continue
		}
		if normalizeMAC(dev.MACAddress) == mac {
			return ip, dev, true
		}
	}
	return "", nil, false
}

// migrateDeviceLocked migriert Daten vom alten IP-Key auf den neuen IP-Key.
// MUSS unter inventoryMutex.Lock() aufgerufen werden!
func migrateDeviceLocked(oldIP, newIP string) {
	oldDev := inventory[oldIP]
	if oldDev == nil {
		delete(inventory, oldIP)
		return
	}

	fmt.Printf("DEDUP: MAC %s migriert %s -> %s\n",
		normalizeMAC(oldDev.MACAddress),
		oldIP,
		newIP,
	)

	newDev := inventory[newIP]
	if newDev == nil {
		newDev = &IPC{IP: newIP}
		inventory[newIP] = newDev
	}

	// Merge: alte Werte übernehmen, wenn neu leer / weniger gut ist
	if newDev.Office == "" {
		newDev.Office = oldDev.Office
	}
	if newDev.Hostname == "" {
		newDev.Hostname = oldDev.Hostname
	}
	if newDev.MACAddress == "" {
		newDev.MACAddress = oldDev.MACAddress
	}
	if newDev.AmsNetID == "" {
		newDev.AmsNetID = oldDev.AmsNetID
	}
	if newDev.OSVersion == "" {
		newDev.OSVersion = oldDev.OSVersion
	}
	if newDev.TwinCATVersion == "" {
		newDev.TwinCATVersion = oldDev.TwinCATVersion
	}
	// Runtime: wenn neu "no Info" ist, aber alt besser, dann alt übernehmen
	if (newDev.RuntimeStatus == "" || newDev.RuntimeStatus == "no Info") &&
		(oldDev.RuntimeStatus != "" && oldDev.RuntimeStatus != "no Info") {
		newDev.RuntimeStatus = oldDev.RuntimeStatus
	}

	// Zeitstempel: "zuletzt online" soll der neueste sein
	if oldDev.LastSeenOnline.After(newDev.LastSeenOnline) {
		newDev.LastSeenOnline = oldDev.LastSeenOnline
	}
	if oldDev.LastUpdate.After(newDev.LastUpdate) {
		newDev.LastUpdate = oldDev.LastUpdate
	}
	if oldDev.LastScan.After(newDev.LastScan) {
		newDev.LastScan = oldDev.LastScan
	}

	// alten IP-Key entfernen
	delete(inventory, oldIP)
}

// dedupByMACIfNeededLocked prüft: ist dieses Online-Gerät bereits unter anderer IP bekannt?
// Wenn ja, migriert es.
// MUSS unter inventoryMutex.Lock() aufgerufen werden!
func dedupByMACIfNeededLocked(currentIP string, mac string) {
	macN := normalizeMAC(mac)
	if macN == "" {
		return
	}

	if oldIP, _, found := findIPByMACLocked(macN, currentIP); found {
		migrateDeviceLocked(oldIP, currentIP)
	}
}
