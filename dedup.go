package main

import (
	"fmt"
	"strings"
)

//
// ------------------------------------------------------------
// MAC-Normalisierung
// ------------------------------------------------------------
//

// normalizeMAC bringt MAC-Adressen immer in ein einheitliches Format:
//
// Ziel:
//
//	AA:BB:CC:DD:EE:FF
//
// Dadurch werden unterschiedliche Schreibweisen vereinheitlicht:
//
//	aa-bb-cc-dd-ee-ff
//	aa:bb:cc:dd:ee:ff
//	AA-BB-CC-DD-EE-FF
//
// => alles wird identisch vergleichbar.
func normalizeMAC(mac string) string {
	mac = strings.TrimSpace(mac)
	mac = strings.ToUpper(mac)
	mac = strings.ReplaceAll(mac, "-", ":")

	return mac
}

//
// ------------------------------------------------------------
// Vorhandenes Gerät über MAC finden
// ------------------------------------------------------------
//

// findIPByMACLocked sucht im Inventory nach einem bereits bekannten Gerät
// mit derselben MAC-Adresse.
//
// exceptIP wird ausgeschlossen,
// damit das aktuell bearbeitete Gerät sich nicht selbst findet.
//
// WICHTIG:
// Diese Funktion darf nur unter inventoryMutex.Lock() verwendet werden.
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

//
// ------------------------------------------------------------
// Gerät von alter IP auf neue IP migrieren
// ------------------------------------------------------------
//

// migrateDeviceLocked verschiebt bekannte Gerätedaten,
// wenn ein Gerät dieselbe MAC hat, aber unter neuer IP auftaucht.
//
// Typischer Fall:
// DHCP / Netzumbau / IP-Wechsel / temporäre Adressänderung
//
// Beispiel:
// altes Gerät unter 172.17.76.15
// erscheint nun unter 172.17.76.37
//
// Dann sollen bekannte Daten erhalten bleiben,
// statt zwei Geräte im Inventory zu haben.
//
// WICHTIG:
// Nur unter inventoryMutex.Lock() verwenden.
func migrateDeviceLocked(oldIP, newIP string) {
	oldDev := inventory[oldIP]

	// Falls alter Eintrag leer ist:
	// alten Key entfernen und abbrechen.
	if oldDev == nil {
		delete(inventory, oldIP)
		return
	}

	fmt.Printf(
		"DEDUP: MAC %s migriert %s -> %s\n",
		normalizeMAC(oldDev.MACAddress),
		oldIP,
		newIP,
	)

	// Zielgerät vorbereiten
	newDev := inventory[newIP]
	if newDev == nil {
		newDev = &IPC{IP: newIP}
		inventory[newIP] = newDev
	}

	//
	// --------------------------------------------------------
	// Merge-Regeln
	// --------------------------------------------------------
	//
	// Prinzip:
	// Bestehende Informationen erhalten,
	// aber nur dort überschreiben, wo neu noch leer ist.
	//

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

	//
	// Runtime-Status:
	// Nur übernehmen, wenn der neue Zustand noch leer
	// oder fachlich schwächer ist.
	//
	if (newDev.RuntimeStatus == "" || newDev.RuntimeStatus == "no Info") &&
		(oldDev.RuntimeStatus != "" && oldDev.RuntimeStatus != "no Info") {
		newDev.RuntimeStatus = oldDev.RuntimeStatus
	}

	//
	// --------------------------------------------------------
	// Zeitstempel übernehmen
	// --------------------------------------------------------
	//
	// Es soll immer der jeweils aktuellste Zeitwert erhalten bleiben.
	//

	if oldDev.LastSeenOnline.After(newDev.LastSeenOnline) {
		newDev.LastSeenOnline = oldDev.LastSeenOnline
	}

	if oldDev.LastUpdate.After(newDev.LastUpdate) {
		newDev.LastUpdate = oldDev.LastUpdate
	}

	if oldDev.LastScan.After(newDev.LastScan) {
		newDev.LastScan = oldDev.LastScan
	}

	//
	// --------------------------------------------------------
	// Alten Key entfernen
	// --------------------------------------------------------
	//
	delete(inventory, oldIP)
}

//
// ------------------------------------------------------------
// Deduplizierung bei neu erkanntem Online-Gerät
// ------------------------------------------------------------
//

// dedupByMACIfNeededLocked prüft:
//
// Ist dieses Gerät bereits unter anderer IP bekannt?
//
// Wenn ja:
// vorhandene Gerätedaten auf aktuelle IP migrieren.
//
// Dadurch bleibt das Inventory stabil,
// auch wenn Geräte ihre IP wechseln.
//
// WICHTIG:
// Nur unter inventoryMutex.Lock() aufrufen.
func dedupByMACIfNeededLocked(currentIP string, mac string) {
	macN := normalizeMAC(mac)

	if macN == "" {
		return
	}

	if oldIP, _, found := findIPByMACLocked(macN, currentIP); found {
		migrateDeviceLocked(oldIP, currentIP)
	}
}
