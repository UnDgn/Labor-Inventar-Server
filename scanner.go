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

// --- 1. DATENSTRUKTUREN ---
type IPC struct {
	IP             string
	IsReachable    bool
	Office         string
	MACAddress     string
	Hostname       string
	OSVersion      string
	AmsNetID       string
	TwinCATVersion string
	RuntimeStatus  string
	LastUpdate     time.Time
}

// UDP-Discovery Ergebnis (Portierung von RemotePlcInfo)
type RemotePlcInfo struct {
	Name        string
	Address     net.IP
	AmsNetID    string
	OsVersion   string
	Fingerprint string
	Comment     string
	TcVersion   AdsVersion
	IsRuntime   string
	Hostname    string
}

type AdsVersion struct {
	Version  uint8
	Revision uint8
	Build    int16
}

func (v AdsVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Version, v.Revision, v.Build)
}

// --- 2. GLOBALE VARIABLEN ---

var (
	inventory      = make(map[string]*IPC)
	inventoryMutex sync.Mutex
	isScanning     bool
)

// --- 3. BASIC FUNKTIONEN (Ping/MAC/DNS) ---

func getMACAddress(ip string) string {
	cmd := exec.Command("arp", "-a", ip)
	output, _ := cmd.Output()
	re := regexp.MustCompile(`([0-9a-fA-F]{2}[:-]){5}([0-9a-fA-F]{2})`)
	mac := re.FindString(string(output))
	return strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
}

func getHostname(ip string) string {
	names, err := net.LookupAddr(ip)
	if err == nil && len(names) > 0 {
		return strings.TrimSuffix(names[0], ".")
	}
	return ""
}

func pingDevice(ip string) bool {
	cmd := exec.Command("ping", "-n", "1", "-w", "800", ip)
	return cmd.Run() == nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --- 5. SCAN LOGIK ---

func runDiscovery() {
	for {
		inventoryMutex.Lock()
		if isScanning {
			inventoryMutex.Unlock()
			time.Sleep(10 * time.Second)
			continue
		}
		isScanning = true
		inventoryMutex.Unlock()

		fmt.Println("Starte Netzwerk-Scan...", time.Now().Format("15:04:05"))

		// Map initialisieren (fix 254)
		inventoryMutex.Lock()
		for i := 1; i <= 254; i++ {
			ip := fmt.Sprintf("172.17.76.%d", i)
			if _, exists := inventory[ip]; !exists {
				inventory[ip] = &IPC{IP: ip, LastUpdate: time.Now()}
			}
		}
		inventoryMutex.Unlock()

		// UDP Discovery (liefert alle Beckhoff-Daten wie C#)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		plcs, derr := discoverPlcsUDP(ctx, 2500*time.Millisecond)
		cancel()

		if derr != nil {
			fmt.Println("ADS UDP discovery error:", derr)
		} else {
			fmt.Printf("ADS UDP discovery found: %d devices\n", len(plcs))
		}

		// Merge UDP Ergebnisse ins Inventory
		inventoryMutex.Lock()
		for _, d := range plcs {
			ipStr := d.Address.String()
			if device, ok := inventory[ipStr]; ok {
				device.AmsNetID = d.AmsNetID
				device.OSVersion = d.OsVersion
				device.TwinCATVersion = d.TcVersion.String()
				device.RuntimeStatus = d.IsRuntime
				// optional: Hostname aus UDP, falls DNS leer
				if device.Hostname == "" && d.Hostname != "" {
					device.Hostname = d.Hostname
				}
			}
		}
		inventoryMutex.Unlock()

		// Ping/MAC/DNS parallel (wie bisher)
		jobs := make(chan string, 254)
		var wg sync.WaitGroup
		for w := 1; w <= 20; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ip := range jobs {
					reachable := pingDevice(ip)

					var mac, host string
					if reachable {
						mac = getMACAddress(ip)
						host = getHostname(ip)
					}

					inventoryMutex.Lock()
					if device, ok := inventory[ip]; ok {
						device.IsReachable = reachable
						device.MACAddress = mac
						// DNS-Hostname überschreibt UDP-Hostname nicht (nur wenn DNS was liefert)
						if host != "" {
							device.Hostname = host
						}
						device.LastUpdate = time.Now()
					}
					inventoryMutex.Unlock()
				}
			}()
		}

		for i := 1; i <= 254; i++ {
			jobs <- fmt.Sprintf("172.17.76.%d", i)
		}
		close(jobs)
		wg.Wait()

		inventoryMutex.Lock()
		isScanning = false
		inventoryMutex.Unlock()

		fmt.Println("Scan abgeschlossen. Warte 2 Minuten...")
		time.Sleep(2 * time.Minute)
	}
}
