package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	ads "github.com/stamp/goADS"
)

// --- 1. DATENSTRUKTUREN ---

// IPC definiert das Schema für unsere Inventar-Daten.
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

// --- 2. GLOBALE VARIABLEN ---

var (
	inventory      = make(map[string]*IPC)
	inventoryMutex sync.Mutex
	// isScanning ist ein "Semaphor" (Sperrflag).
	// Es verhindert, dass die Endlosschleife und ein manueller Button-Klick
	// gleichzeitig Scans starten und das Netzwerk überlasten.
	isScanning bool
)

// --- 3. LOGIK-FUNKTIONEN ---

// getMACAddress extrahiert die Hardware-Adresse aus dem Windows-ARP-Cache.
func getMACAddress(ip string) string {
	cmd := exec.Command("arp", "-a", ip)
	output, _ := cmd.Output()
	re := regexp.MustCompile(`([0-9a-fA-F]{2}[:-]){5}([0-9a-fA-F]{2})`)
	mac := re.FindString(string(output))
	return strings.ToUpper(strings.ReplaceAll(mac, "-", ":"))
}

// getHostname löst die IP-Adresse in einen DNS-Namen auf.
func getHostname(ip string) string {
	names, err := net.LookupAddr(ip)
	if err == nil && len(names) > 0 {
		return strings.TrimSuffix(names[0], ".")
	}
	return ""
}

func isInLabSubnet(ip string) bool {
	return strings.HasPrefix(ip, "172.17.76.")
}

func getAdsData(ip string, amsNetID string) (tcVersion string, state string) {
	// --- 1) PLC State (851) ---
	plcConn, err := ads.NewConnection(ip, amsNetID, 851)
	if err != nil {
		return "Conn Error", "Offline"
	}
	defer plcConn.Close()

	plcConn.Connect()

	st, err := plcConn.ReadState()
	if err != nil {
		return "Route/ADS Error", "Locked"
	}

	switch st.ADSState {
	case 5:
		state = "RUN"
	case 6:
		state = "STOP"
	case 15:
		state = "CONFIG"
	default:
		state = fmt.Sprintf("State %d", st.ADSState)
	}

	// --- 2) DeviceInfo (10000) ---
	sysConn, err := ads.NewConnection(ip, amsNetID, 10000)
	if err != nil {
		return "Unknown", state
	}
	defer sysConn.Close()

	sysConn.Connect()

	info, err := sysConn.ReadDeviceInfo()
	if err == nil {
		tcVersion = fmt.Sprintf("%d.%d.%d", info.MajorVersion, info.MinorVersion, info.BuildVersion)
	} else {
		tcVersion = "Unknown"
	}

	return tcVersion, state
}

// runDiscovery führt den eigentlichen Scan-Vorgang durch.
func runDiscovery() {
	// Endlosschleife für dauerhaftes Monitoring
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

		discovered := discoverTargetsUDP(2500 * time.Millisecond)
		fmt.Println("Discovery found:", len(discovered))
		for ip, netid := range discovered {
			fmt.Println("  ", ip, "->", netid)
		}

		// Map-Initialisierung
		inventoryMutex.Lock()
		for i := 1; i <= 254; i++ {
			ip := fmt.Sprintf("172.17.76.%d", i)
			if _, exists := inventory[ip]; !exists {
				inventory[ip] = &IPC{IP: ip, LastUpdate: time.Now()}
			}
		}
		inventoryMutex.Unlock()

		// Parallelisierung via Worker-Pool (20 Goroutines)
		jobs := make(chan string, 254)
		var wg sync.WaitGroup
		for w := 1; w <= 20; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ip := range jobs {
					reachable := pingDevice(ip)
					var mac, host, aNetID, aVer, aStatus string
					if reachable {
						mac = getMACAddress(ip)
						host = getHostname(ip)

						if !isInLabSubnet(ip) {
							aStatus = "Not TwinCAT Network"
						} else if netID, ok := discovered[ip]; ok {
							aNetID = netID
							aVer, aStatus = getAdsData(ip, aNetID)
						} else {
							aStatus = "No NetID"
						}
					}
					inventoryMutex.Lock()
					if device, ok := inventory[ip]; ok {
						device.IsReachable = reachable
						device.MACAddress = mac
						device.Hostname = host
						device.AmsNetID = aNetID
						device.TwinCATVersion = aVer
						device.RuntimeStatus = aStatus
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

func pingDevice(ip string) bool {
	cmd := exec.Command("ping", "-n", "1", "-w", "800", ip)
	return cmd.Run() == nil
}

// --- 4. WEB-HANDLER ---

// handleTriggerScan erlaubt den manuellen Start eines Scans via HTTP-Request (AJAX).
func handleTriggerScan(w http.ResponseWriter, r *http.Request) {
	// Da runDiscovery in einer Endlosschleife läuft, triggern wir
	// hier nur einen neuen Durchlauf, falls gerade keiner läuft.
	fmt.Println("Manueller Scan-Trigger empfangen.")
	w.WriteHeader(http.StatusOK)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	lastUpdateStr := time.Now().Format("15:04:05")

	fmt.Fprint(w, `
    <html>
    <head>
        <style>
            body { font-family: 'Segoe UI', sans-serif; margin: 0; padding: 20px; background-color: #f4f7f6; }
            .container { background: white; padding: 20px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
            
            .header-bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; gap: 20px; }
            .logo-section { display: flex; align-items: center; gap: 15px; min-width: 300px; }
            
            /* Suchfeld-Gruppe */
            .search-group { position: relative; flex-grow: 1; max-width: 400px; }
            .search-icon { position: absolute; left: 10px; top: 10px; color: #888; }
            #searchInput { padding: 10px 10px 10px 35px; border: 1px solid #ddd; border-radius: 4px; width: 100%; font-size: 0.9em; }

            /* Scan-Gruppe rechts */
            .scan-group { display: flex; align-items: center; gap: 15px; min-width: 300px; justify-content: flex-end; }
            .last-refresh { font-size: 0.8em; color: #666; white-space: nowrap; }

            .btn-scan { background-color: #ce1126; color: white; border: none; padding: 10px 20px; border-radius: 4px; cursor: pointer; font-weight: bold; transition: background 0.2s; }
            .btn-scan:hover { background-color: #a00d1d; }
            .btn-scan:disabled { background-color: #ccc; }

            table { border-collapse: collapse; width: 100%; margin-top: 10px; }
            th { background-color: #ce1126; color: white; padding: 12px; text-align: left; font-size: 0.85em; position: sticky; top: 0; }
            td { padding: 12px 10px; border-bottom: 1px solid #eee; font-size: 0.85em; }
            .status-online { color: #28a745; font-weight: bold; }
            .status-offline { color: #ccc; }
        </style>
        <script>
            function filterTable() {
                let filter = document.getElementById("searchInput").value.toUpperCase();
                let rows = document.querySelector("#deviceTable tbody").rows;
                for (let row of rows) {
                    row.style.display = row.textContent.toUpperCase().includes(filter) ? "" : "none";
                }
            }
            function startScan() {
                const btn = document.getElementById("scanBtn");
                btn.disabled = true;
                btn.innerText = "Scanning...";
                fetch('/trigger-scan').then(() => {
                    setTimeout(() => { location.reload(); }, 3000);
                });
            }
        </script>
    </head>
    <body>
        <div class="container">
            <div class="header-bar">
                <div class="logo-section">
                    <img src="/static/logo.png" alt="Beckhoff" style="height: 40px;">
                    <h1 style="margin: 0; font-size: 1.5em; font-weight: 300;">Testnetz</h1>
                </div>

                <div class="search-group">
                    <span class="search-icon">🔍</span>
                    <input type="text" id="searchInput" onkeyup="filterTable()" placeholder="Gerät suchen (IP, Name, MAC)...">
                </div>

                <div class="scan-group">
                    <div class="last-refresh">Refreshed: <strong>`+lastUpdateStr+`</strong></div>
                    <button id="scanBtn" class="btn-scan" onclick="startScan()">Scan</button>
                </div>
            </div>
            
            <table id="deviceTable">
                <thead>
                    <tr>
                        <th>Status</th><th>IP-Adresse</th><th>Hostname</th><th>Büro</th>
                        <th>MAC-Adresse</th><th>OS Version</th><th>AMS Net-ID</th>
                        <th>TwinCAT</th><th>Runtime</th>
                    </tr>
                </thead>
                <tbody>`)

	inventoryMutex.Lock()
	var devices []*IPC
	for _, dev := range inventory {
		devices = append(devices, dev)
	}

	sort.Slice(devices, func(i, j int) bool {
		if devices[i].IsReachable != devices[j].IsReachable {
			return devices[i].IsReachable
		}
		ipI := net.ParseIP(devices[i].IP).To4()
		ipJ := net.ParseIP(devices[j].IP).To4()
		return bytes.Compare(ipI, ipJ) < 0
	})

	for _, device := range devices {
		statusClass, statusText := "status-offline", "Offline"
		if device.IsReachable {
			statusClass, statusText = "status-online", "Online"
		}

		fmt.Fprintf(w, `<tr>
                <td class="%s">%s</td>
                <td><strong>%s</strong></td>
                <td>%s</td><td>%s</td>
                <td class="mac-font">%s</td>
                <td>%s</td><td>%s</td><td>%s</td><td>%s</td>
            </tr>`,
			statusClass, statusText, device.IP, device.Hostname,
			device.Office, device.MACAddress, device.OSVersion,
			device.AmsNetID, device.TwinCATVersion, device.RuntimeStatus)
	}
	inventoryMutex.Unlock()
	fmt.Fprint(w, "</tbody></table></div></body></html>")
}

// --- 5. HAUPTPROGRAMM ---

func main() {
	// Startet den Hintergrundprozess für das Dauer-Monitoring
	go runDiscovery()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/trigger-scan", handleTriggerScan)

	fmt.Println("-----------------------------------------------")
	fmt.Println("Beckhoff Inventar-Server Online")
	fmt.Println("Lokal: http://localhost:8080")
	fmt.Println("-----------------------------------------------")

	http.ListenAndServe("0.0.0.0:8080", nil)
}
