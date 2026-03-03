package main

import ( //--------------------------------------------------------------Onlinesetzen bei UDP gerät finden ergänzen!!! Wenn ping wegen firewall nicht findet----------------
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

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
	LastScan       time.Time // wann zuletzt geprüft (egal ob online)
	LastSeenOnline time.Time // wann zuletzt online gesehen
}

var (
	inventory      = make(map[string]*IPC)
	inventoryMutex sync.Mutex
	isScanning     bool
	scanTrigger    = make(chan struct{}, 1) // gepuffert: 1 = "scan requested"
)

// --- Ping/MAC/DNS ---

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

// --- Scan ---

func runDiscovery() {
	select {
	case scanTrigger <- struct{}{}:
	default:
	}
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		// warten auf automatischen Tick oder manuellen Trigger
		select {
		case <-ticker.C:
			fmt.Println("Automatischer Scan ausgelöst")
		case <-scanTrigger:
			fmt.Println("Manueller Scan wird gestartet")
		}

		// Semaphore
		inventoryMutex.Lock()
		if isScanning {
			inventoryMutex.Unlock()
			continue
		}
		isScanning = true
		inventoryMutex.Unlock()

		fmt.Println("Starte Netzwerk-Scan...", time.Now().Format("15:04:05"))

		// Inventory init (fix 254)
		inventoryMutex.Lock()
		for i := 1; i <= 254; i++ {
			ip := fmt.Sprintf("172.17.76.%d", i)
			if _, exists := inventory[ip]; !exists {
				inventory[ip] = &IPC{IP: ip, LastUpdate: time.Now()}
			}
		}
		inventoryMutex.Unlock()

		// UDP discovery
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		plcs, derr := discoverPlcsUDP(ctx, 2500*time.Millisecond)
		cancel()

		if derr != nil {
			fmt.Println("ADS UDP discovery error:", derr)
		} else {
			fmt.Printf("ADS UDP discovery found: %d devices\n", len(plcs))
		}

		// Merge UDP info
		inventoryMutex.Lock()
		for _, d := range plcs {
			ipStr := d.Address.String()
			if dev, ok := inventory[ipStr]; ok {
				dev.AmsNetID = d.AmsNetID
				dev.OSVersion = d.OsVersion
				dev.TwinCATVersion = d.TcVersion.String()
				dev.RuntimeStatus = d.IsRuntime // Basis: UDP (TC3 oft "no Info")
				if dev.Hostname == "" && d.Hostname != "" {
					dev.Hostname = d.Hostname
				}
				dev.LastUpdate = time.Now()
			}
		}
		inventoryMutex.Unlock()

		// ADS Route + ReadState (RUN/STOP): nur für UDP gefundene Targets
		localIP, _, lipErr := getLocalLabIPv4()
		if lipErr != nil {
			fmt.Println("Local IP error:", lipErr)
		} else {
			type job struct {
				ip    string
				netid string
			}
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
			const showErrorsInUI = true // zum Debug; später auf false

			jobs := make(chan job, len(targets))
			var wg sync.WaitGroup

			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for j := range jobs {
						remoteIP := net.ParseIP(j.ip).To4()
						if remoteIP == nil {
							continue
						}

						cctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
						res := TryReadPlcState(cctx, localIP, remoteIP, j.netid)
						cancel()

						inventoryMutex.Lock()
						if dev, ok := inventory[j.ip]; ok {
							if res.Err == "" && res.Status != "" {
								dev.RuntimeStatus = res.Status // RUN/STOP/...
							} else if showErrorsInUI {
								dev.RuntimeStatus = fmt.Sprintf("%s (%s)", res.Status, res.Err)
							}
							dev.LastUpdate = time.Now()
						}
						inventoryMutex.Unlock()
					}
				}()
			}

			for _, t := range targets {
				jobs <- t
			}
			close(jobs)
			wg.Wait()
		}

		// Ping/MAC/DNS parallel
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
					if dev, ok := inventory[ip]; ok {
						dev.IsReachable = reachable
						dev.LastScan = now

						if reachable {
							dev.LastSeenOnline = now
							if mac != "" {
								dev.MACAddress = mac
							}
							if host != "" {
								dev.Hostname = host
							}
						}
					}
					inventoryMutex.Unlock()
				}
			}()
		}

		for i := 1; i <= 254; i++ {
			pingJobs <- fmt.Sprintf("172.17.76.%d", i)
		}
		close(pingJobs)
		wgPing.Wait()

		// Semaphore frei
		inventoryMutex.Lock()
		isScanning = false
		inventoryMutex.Unlock()
		if err := saveSnapshot(); err != nil {
			fmt.Println("Snapshot save error:", err)
		}

		fmt.Println("Scan abgeschlossen.", time.Now().Format("15:04:05"))
	}
}
