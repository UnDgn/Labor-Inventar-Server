package main

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// IPC beschreibt ein einzelnes Gerät im Inventar.
// Die Struktur wird während des Scans nach und nach mit Informationen gefüllt.
type IPC struct {
	IP             string    // IPv4-Adresse im Testnetz
	IsReachable    bool      // Ergebnis des Ping-Checks
	Office         string    // Büro-/Standortzuordnung anhand der MAC
	Comment        string    // Freitext-Kommentar anhand der MAC
	MACAddress     string    // MAC-Adresse aus ARP
	Hostname       string    // Reverse-DNS / Hostname
	OSVersion      string    // OS-Information aus ADS-UDP-Discovery
	AmsNetID       string    // ADS Net ID des Geräts
	TwinCATVersion string    // TwinCAT-Version aus ADS-UDP-Discovery
	RuntimeStatus  string    // aktuell gelesener TwinCAT-/Runtime-Status
	DeviceType     string    // optional für spätere Typisierung
	LastUpdate     time.Time // wann ADS-/Discovery-Daten zuletzt aktualisiert wurden
	LastScan       time.Time // wann zuletzt überhaupt geprüft wurde
	LastSeenOnline time.Time // wann das Gerät zuletzt als online erkannt wurde
	RouteKnownGood bool      // ob ADS-Route schon einmal erfolgreich gesetzt wurde
	LastRouteOK    time.Time // wann die ADS-Route zuletzt erfolgreich war
	RuntimePort    uint16    // zuletzt erfolgreicher ADS-Port (für spätere Erweiterungen)
}

var (
	// inventory enthält alle bekannten Geräte, Key = IP-Adresse.
	inventory = make(map[string]*IPC)

	// inventoryMutex schützt inventory und andere gemeinsam genutzte Zustände.
	inventoryMutex sync.Mutex

	// isScanning verhindert, dass zwei vollständige Scans gleichzeitig laufen.
	isScanning bool

	// scanTrigger startet einen Scan manuell oder beim Programmstart.
	// Gepuffert mit 1, damit doppelte Trigger nicht auflaufen.
	scanTrigger = make(chan struct{}, 1)
)

// ------------------------------------------------------------
// Hilfsfunktionen für Ping / MAC / Hostname
// ------------------------------------------------------------

// getMACAddress liest per "arp -a <ip>" die MAC-Adresse aus der lokalen ARP-Tabelle.
// Rückgabeformat wird auf "AA:BB:CC:DD:EE:FF" normalisiert.
func getMACAddress(ip string) string {
	cmd := exec.Command("arp", "-a", ip)
	output, _ := cmd.Output()

	re := regexp.MustCompile(`([0-9a-fA-F]{2}[:-]){5}([0-9a-fA-F]{2})`)
	mac := re.FindString(string(output))

	return strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
}

// getHostname versucht per Reverse-DNS den Hostnamen zur IP aufzulösen.
func getHostname(ip string) string {
	names, err := net.LookupAddr(ip)
	if err == nil && len(names) > 0 {
		return strings.TrimSuffix(names[0], ".")
	}
	return ""
}

// pingDevice prüft per Windows-Ping, ob ein Gerät erreichbar ist.
// "-n 1" = genau ein Ping
// "-w 800" = Timeout 800 ms
func pingDevice(ip string) bool {
	cmd := exec.Command("ping", "-n", "1", "-w", "800", ip)
	return cmd.Run() == nil
}

// ------------------------------------------------------------
// Haupt-Scanroutine
// ------------------------------------------------------------

// runDiscovery läuft dauerhaft in einer Goroutine.
// Sie startet einen Scan:
// - direkt beim Start
// - danach periodisch über den Ticker
// - zusätzlich manuell über scanTrigger
func runDiscovery() {
	// Direkt zu Beginn einen initialen Scan anfordern.
	select {
	case scanTrigger <- struct{}{}:
	default:
	}

	// Automatischer Wiederholungsscan alle 10 Minuten.
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		// Auf automatischen Tick oder manuellen Trigger warten.
		select {
		case <-ticker.C:
			fmt.Println("Automatischer Scan ausgelöst")
		case <-scanTrigger:
			fmt.Println("Manueller Scan wird gestartet")
		}

		// Verhindern, dass zwei Vollscans gleichzeitig laufen.
		inventoryMutex.Lock()
		if isScanning {
			inventoryMutex.Unlock()
			continue
		}
		isScanning = true
		inventoryMutex.Unlock()

		fmt.Println("Starte Netzwerk-Scan...", time.Now().Format("15:04:05"))

		// --------------------------------------------------------
		// 1) Grundgerüst für alle IPs im Testnetz anlegen
		// --------------------------------------------------------

		// So existiert für jede IP 172.17.76.1 bis 172.17.76.254
		// ein Eintrag im Inventory, auch wenn noch keine Details bekannt sind.
		inventoryMutex.Lock()
		for i := 1; i <= 254; i++ {
			ip := fmt.Sprintf("172.17.76.%d", i)
			if _, exists := inventory[ip]; !exists {
				inventory[ip] = &IPC{
					IP:         ip,
					LastUpdate: time.Now(),
				}
			}
		}
		inventoryMutex.Unlock()

		// --------------------------------------------------------
		// 2) ADS UDP Discovery
		// --------------------------------------------------------

		// Hier werden Beckhoff-/TwinCAT-Geräte per UDP-Broadcast gefunden.
		// Diese Phase liefert u. a.:
		// - AMS Net ID
		// - OS-Version
		// - TwinCAT-Version
		// - teilweise grobe Runtime-Hinweise
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		plcs, derr := discoverPlcsUDP(ctx, 2500*time.Millisecond)
		cancel()

		if derr != nil {
			fmt.Println("ADS UDP discovery error:", derr)
		} else {
			fmt.Printf("ADS UDP discovery found: %d devices\n", len(plcs))
		}

		//
		// --------------------------------------------------------
		// 3) UDP-Discovery-Daten ins Inventory übernehmen
		// --------------------------------------------------------
		//
		// Hier wird der aktuelle Kenntnisstand aus dem UDP-Fund
		// in die vorhandenen Inventory-Einträge gemerged.
		inventoryMutex.Lock()
		for _, d := range plcs {
			ipStr := d.Address.String()

			if dev, ok := inventory[ipStr]; ok {
				dev.AmsNetID = d.AmsNetID
				dev.OSVersion = d.OsVersion
				dev.TwinCATVersion = d.TcVersion.String()

				// Basiswert aus UDP-Discovery.
				// Dieser wird später ggf. durch echten ADS ReadState überschrieben.
				dev.RuntimeStatus = d.IsRuntime

				// Hostname aus UDP nur übernehmen, wenn noch keiner bekannt ist.
				if dev.Hostname == "" && d.Hostname != "" {
					dev.Hostname = d.Hostname
				}

				dev.LastUpdate = time.Now()
			}
		}
		inventoryMutex.Unlock()

		// --------------------------------------------------------
		// 4) TwinCAT-/ADS-State aktiv abfragen
		// --------------------------------------------------------

		// Für alle per UDP gefundenen Geräte wird versucht:
		// - eine ADS-Route anzulegen
		// - anschließend per ADS den TwinCAT-/Runtime-State zu lesen

		// Hinweis:
		// Diese Phase läuft parallel mit Worker-Goroutines.
		localIP, _, lipErr := getLocalLabIPv4()
		if lipErr != nil {
			fmt.Println("Local IP error:", lipErr)
		} else {
			// job beschreibt ein einzelnes Ziel für die ADS-State-Abfrage.
			type job struct {
				ip    string
				netid string
			}

			// Alle UDP-gefundenen Ziele mit AMS Net ID einsammeln.
			targets := make([]job, 0, len(plcs))
			for _, d := range plcs {
				if d.AmsNetID == "" {
					continue
				}

				targets = append(targets, job{
					ip:    d.Address.String(),
					netid: d.AmsNetID,
				})
			}

			const workers = 8
			const showErrorsInUI = true

			jobs := make(chan job, len(targets))
			var wg sync.WaitGroup

			// Workerpool für ADS-State-Abfragen.
			for w := 0; w < workers; w++ {
				wg.Add(1)

				go func() {
					defer wg.Done()

					for j := range jobs {
						remoteIP := net.ParseIP(j.ip).To4()
						if remoteIP == nil {
							continue
						}

						// Einzelnes Gerät mit Timeout abfragen.
						cctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
						res := TryReadTCState(cctx, localIP, remoteIP, j.netid)
						cancel()

						// Ergebnis ins Inventory zurückschreiben.
						inventoryMutex.Lock()
						if dev, ok := inventory[j.ip]; ok {
							if res.Err == "" && res.Status != "" {
								// Erfolgreicher ADS-State-Read.
								dev.RuntimeStatus = res.Status

								// Erfolgreichen Port merken (für spätere Erweiterungen / Optimierungen).
								if res.RuntimePort != 0 {
									dev.RuntimePort = res.RuntimePort
								}

								dev.RouteKnownGood = true
								dev.LastRouteOK = time.Now()
							} else if showErrorsInUI {
								// Bekannter Spezialfall:
								// 0x00000006 bedeutet praktisch meistens:
								// "der angefragte Runtime-Port existiert dort nicht".
								if strings.Contains(res.Err, "0x00000006") {
									dev.RuntimeStatus = "No Runtime"
								} else {
									// Alle anderen Fehler zunächst direkt sichtbar machen.
									dev.RuntimeStatus = fmt.Sprintf("%s (%s)", res.Status, res.Err)
								}
							}
						}
						inventoryMutex.Unlock()
					}
				}()
			}

			// Jobs an die Worker verteilen.
			for _, t := range targets {
				jobs <- t
			}
			close(jobs)

			wg.Wait()
		}

		// --------------------------------------------------------
		// 5) Ping / MAC / Hostname parallel ergänzen
		// --------------------------------------------------------

		// Diese Phase läuft unabhängig von ADS:
		// - Ping: ist das Gerät aktuell erreichbar?
		// - ARP: welche MAC hat es?
		// - Reverse DNS: welcher Hostname ist bekannt?
		pingJobs := make(chan string, 254)
		var wgPing sync.WaitGroup

		for w := 1; w <= 20; w++ {
			wgPing.Add(1)

			go func() {
				defer wgPing.Done()

				for ip := range pingJobs {
					reachable := pingDevice(ip)

					var mac, host string
					if reachable {
						mac = getMACAddress(ip)
						host = getHostname(ip)
					}

					now := time.Now()

					inventoryMutex.Lock()

					// Wenn das Gerät online ist und wir eine MAC haben,
					// prüfen wir, ob es bereits unter anderer IP existiert
					// und ggf. per MAC dedupliziert werden muss.
					if reachable && mac != "" {
						dedupByMACIfNeededLocked(ip, mac)
					}

					// Gerätedaten aktualisieren.
					if dev, ok := inventory[ip]; ok {
						dev.IsReachable = reachable
						dev.LastScan = now

						if reachable {
							dev.LastSeenOnline = now

							if mac != "" {
								dev.MACAddress = normalizeMAC(mac)
							}
							if host != "" {
								dev.Hostname = host
							}
						}

						// Zusatzinformationen anhand der MAC nachladen.
						if dev.MACAddress != "" {
							dev.Office = getOfficeForMAC(dev.MACAddress)
							dev.Comment = getCommentForMAC(dev.MACAddress)
						}
					}

					inventoryMutex.Unlock()
				}
			}()
		}

		// Alle 254 IPs des Testnetzes in die Ping-Queue legen.
		for i := 1; i <= 254; i++ {
			pingJobs <- fmt.Sprintf("172.17.76.%d", i)
		}
		close(pingJobs)

		wgPing.Wait()

		// --------------------------------------------------------
		// 6) Scan sauber beenden und Snapshot speichern
		// --------------------------------------------------------

		inventoryMutex.Lock()
		isScanning = false
		inventoryMutex.Unlock()

		if err := saveSnapshot(); err != nil {
			fmt.Println("Snapshot save error:", err)
		}

		fmt.Println("Scan abgeschlossen.", time.Now().Format("15:04:05"))
	}
}
