package main

import (
	"bytes"
	"context"
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

// --- 4. ADS UDP DISCOVERY (48899) ---
// Minimal + robust: wir lesen zumindest AMS NetID aus der Antwort.
// (Wenn du OS/TwinCAT/Hostname per UDP brauchst, erweitern wir später.)

var (
	segHeader           = []byte{0x03, 0x66, 0x14, 0x71}
	segEnd              = []byte{0, 0, 0, 0}
	segRequestDiscover  = []byte{1, 0, 0, 0}
	segPort             = []byte{0x10, 0x27} // 0x2710 = 10000 little endian (wie Beckhoff Request)
	segResponseDiscover = []byte{1, 0, 0, 0x80}
	segAmsTemplate      = []byte{0, 0, 0, 0, 1, 1} // erste 4 Bytes werden mit localIP überschrieben
)

func isInLabSubnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 172 && ip4[1] == 17 && ip4[2] == 76
}

// nimmt exakt eine lokale IP im 172.17.76.0/24 Netz (oder error)
func getLocalLabIPv4() (net.IP, net.IPMask, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			if isInLabSubnetIP(ip4) {
				return ip4, ipnet.Mask, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("no local IPv4 found in 172.17.76.0/24")
}

func broadcastAddr(ip net.IP, mask net.IPMask) (net.IP, error) {
	ip4 := ip.To4()
	if ip4 == nil || len(mask) != 4 {
		return nil, fmt.Errorf("need IPv4 + 4-byte mask")
	}
	out := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		out[i] = ip4[i] | ^mask[i]
	}
	return out, nil
}

func buildDiscoverRequest(localIP net.IP) []byte {
	ip4 := localIP.To4()
	ams := make([]byte, 6)
	copy(ams, segAmsTemplate)
	if ip4 != nil {
		copy(ams[0:4], ip4)
	}

	var out []byte
	out = append(out, segHeader...)
	out = append(out, segEnd...)
	out = append(out, segRequestDiscover...)
	out = append(out, ams...)
	out = append(out, segPort...)
	out = append(out, segEnd...)
	return out
}

// Antwort: mindestens AMS NetID extrahieren
func parseDiscoverResponse(pkt []byte) (amsNetID string, ok bool) {
	if len(pkt) < 18 {
		return "", false
	}
	if !bytes.Equal(pkt[0:4], segHeader) {
		return "", false
	}
	if !bytes.Equal(pkt[4:8], segEnd) {
		return "", false
	}
	if !bytes.Equal(pkt[8:12], segResponseDiscover) {
		return "", false
	}

	shift := 12
	ams := pkt[shift : shift+6]
	return fmt.Sprintf("%d.%d.%d.%d.%d.%d", ams[0], ams[1], ams[2], ams[3], ams[4], ams[5]), true
}

// liefert map[ip]amsNetID
func discoverTargetsUDP(ctx context.Context, timeout time.Duration) (map[string]string, error) {
	found := make(map[string]string)

	localIP, mask, err := getLocalLabIPv4()
	if err != nil {
		return found, err
	}
	bc, err := broadcastAddr(localIP, mask)
	if err != nil {
		return found, err
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: localIP, Port: 0})
	if err != nil {
		return found, err
	}
	defer conn.Close()

	req := buildDiscoverRequest(localIP)

	_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_, err = conn.WriteToUDP(req, &net.UDPAddr{IP: bc, Port: 48899})
	if err != nil {
		return found, err
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			return found, ctx.Err()
		default:
		}

		_ = conn.SetReadDeadline(deadline)
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			return found, err
		}
		ams, ok := parseDiscoverResponse(buf[:n])
		if !ok {
			continue
		}
		found[raddr.IP.String()] = ams
	}

	return found, nil
}

func defaultAmsFromIP(ip string) string {
	return ip + ".1.1"
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

		// ---- ADS UDP Discovery (einmal pro Scan) ----
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		discovered, derr := discoverTargetsUDP(ctx, 2500*time.Millisecond)
		cancel()

		if derr != nil {
			fmt.Println("ADS discovery error:", derr)
		} else {
			fmt.Printf("ADS discovery found: %d devices\n", len(discovered))
			// Debug: zeig ein paar
			c := 0
			for ip, ams := range discovered {
				fmt.Println("  ", ip, "->", ams)
				c++
				if c >= 5 {
					break
				}
			}
		}

		// Worker pool ping/MAC/DNS + (NetID setzen)
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

					// ADS: NetID aus discovery, fallback auf ip.1.1 (nur damit du sofort was siehst)
					aNetID := ""
					if netid, ok := discovered[ip]; ok {
						aNetID = netid
					} else if reachable {
						// Fallback nur für reachable, damit es nicht alles “zumüllt”
						aNetID = defaultAmsFromIP(ip)
					}

					inventoryMutex.Lock()
					if device, ok := inventory[ip]; ok {
						device.IsReachable = reachable
						device.MACAddress = mac
						device.Hostname = host
						if aNetID != "" {
							device.AmsNetID = aNetID
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

// --- 6. WEB HANDLER ---

func handleTriggerScan(w http.ResponseWriter, r *http.Request) {
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

            .search-group { position: relative; flex-grow: 1; max-width: 400px; }
            .search-icon { position: absolute; left: 10px; top: 10px; color: #888; }
            #searchInput { padding: 10px 10px 10px 35px; border: 1px solid #ddd; border-radius: 4px; width: 100%; font-size: 0.9em; }

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

// --- 7. MAIN ---

func main() {
	go runDiscovery()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/trigger-scan", handleTriggerScan)

	fmt.Println("-----------------------------------------------")
	fmt.Println("Beckhoff Inventar-Server Online")
	fmt.Println("Lokal: http://localhost:8080")
	fmt.Println("-----------------------------------------------")

	_ = http.ListenAndServe("0.0.0.0:8080", nil)
}
