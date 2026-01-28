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

// --- 2. GLOBALE VARIABLEN ---

var (
	inventory      = make(map[string]*IPC)
	inventoryMutex sync.Mutex
)

// --- 3. LOGIK-FUNKTIONEN ---

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

func runDiscovery() {
	// Endlosschleife für kontinuierliche Scans
	for {
		fmt.Println("Starte Netzwerk-Scan...", time.Now().Format("15:04:05"))

		// Map initial befüllen falls leer
		inventoryMutex.Lock()
		for i := 1; i <= 254; i++ {
			ip := fmt.Sprintf("172.17.76.%d", i)
			if _, exists := inventory[ip]; !exists {
				inventory[ip] = &IPC{IP: ip, LastUpdate: time.Now()}
			}
		}
		inventoryMutex.Unlock()

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
						device.Hostname = host
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
		fmt.Println("Scan abgeschlossen. Warte auf nächsten Durchlauf...")
		time.Sleep(2 * time.Minute) // Kurze Pause zwischen den Scans
	}
}

func pingDevice(ip string) bool {
	cmd := exec.Command("ping", "-n", "1", "-w", "800", ip)
	return cmd.Run() == nil
}

// --- 4. WEB-HANDLER ---

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Zeitstempel für die Anzeige oben rechts
	lastUpdateStr := time.Now().Format("15:04:05")

	fmt.Fprint(w, `
    <html>
    <head>
        <style>
            body { font-family: 'Segoe UI', sans-serif; margin: 0; padding: 20px; background-color: #f4f7f6; }
            .container { background: white; padding: 20px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
            
            /* Kopfzeile Styling */
            .header-bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 30px; }
            .logo-section { display: flex; align-items: center; gap: 20px; }
            .last-scan-info { font-size: 0.85em; color: #666; font-style: italic; }

            table { border-collapse: collapse; width: 100%; margin-top: 20px; }
            th { background-color: #ce1126; color: white; padding: 12px; text-align: left; font-size: 0.85em; position: sticky; top: 0; }
            td { padding: 10px; border-bottom: 1px solid #eee; font-size: 0.85em; }
            .status-online { color: #28a745; font-weight: bold; }
            .status-offline { color: #ccc; }
            .mac-font { font-family: monospace; color: #666; }
        </style>
        <meta http-equiv="refresh" content="15">
    </head>
    <body>
        <div class="container">
            <div class="header-bar">
                <div class="logo-section">
                    <img src="/static/logo.png" alt="Beckhoff" style="height: 45px;">
                    <h1 style="margin: 0; font-weight: 300;">Labor-Inventarisierung</h1>
                </div>
                <div class="last-scan-info">Letzter UI-Refresh: `+lastUpdateStr+`</div>
            </div>

            <table>
                <tr>
                    <th>Status</th>
                    <th>IP-Adresse</th>
                    <th>Hostname</th>
                    <th>Büro</th>
                    <th>MAC-Adresse</th>
                    <th>OS Version</th>
                    <th>AMS Net-ID</th>
                    <th>TwinCAT</th>
                    <th>Runtime</th>
                </tr>`)

	inventoryMutex.Lock()

	// 1. Liste für Sortierung vorbereiten
	var devices []*IPC
	for _, dev := range inventory {
		devices = append(devices, dev)
	}

	// 2. Sortierung (Online zuerst, dann nach IP)
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].IsReachable != devices[j].IsReachable {
			return devices[i].IsReachable
		}
		// Numerischer Vergleich der IPs (benötigt "bytes" Paket und To4())
		ipI := net.ParseIP(devices[i].IP).To4()
		ipJ := net.ParseIP(devices[j].IP).To4()
		return bytes.Compare(ipI, ipJ) < 0
	})

	// 3. Tabellenzeilen ausgeben
	for _, device := range devices {
		statusClass := "status-offline"
		statusText := "Offline"
		if device.IsReachable {
			statusClass = "status-online"
			statusText = "Online"
		}

		fmt.Fprintf(w, `<tr>
                <td class="%s">%s</td>
                <td><strong>%s</strong></td>
                <td>%s</td>
                <td>%s</td>
                <td class="mac-font">%s</td>
                <td>%s</td>
                <td>%s</td>
                <td>%s</td>
                <td>%s</td>
            </tr>`,
			statusClass, statusText, device.IP, device.Hostname,
			device.Office, device.MACAddress, device.OSVersion,
			device.AmsNetID, device.TwinCATVersion, device.RuntimeStatus)
	}
	inventoryMutex.Unlock()

	fmt.Fprint(w, "</table></div></body></html>")
}

// --- 5. HAUPTPROGRAMM ---

func main() {
	go runDiscovery()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", handleDashboard)

	port := "8080"
	address := "0.0.0.0:" + port

	fmt.Println("-----------------------------------------------")
	fmt.Println("Beckhoff Inventar-Server wird gestartet...")
	fmt.Printf("Lokal erreichbar:    http://localhost:%s\n", port)
	fmt.Printf("Im Netzwerk über:    http://172.17.76.162:%s\n", port)
	fmt.Println("-----------------------------------------------")

	err := http.ListenAndServe(address, nil)
	if err != nil {
		fmt.Printf("FEHLER: %s\n", err)
	}
}
