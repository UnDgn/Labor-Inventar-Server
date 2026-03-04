package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"sort"
	"time"
)

// --- 6. WEB HANDLER ---

func handleTriggerScan(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Manueller Scan-Trigger empfangen.")

	// Nicht blockieren, wenn schon ein Trigger ansteht
	select {
	case scanTrigger <- struct{}{}:
	default:
	}

	w.WriteHeader(http.StatusOK)
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	lastUpdateStr := time.Now().Format("15:04:05")

	// 1) Grund-HTML bis Header-Bar (OHNE Tabelle)
	fmt.Fprint(w, `
    <html>
    <head>
        <style>
            body { font-family: 'Segoe UI', sans-serif; margin: 0; padding: 20px; background-color: #f4f7f6; }
            .container { background: white; padding: 20px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }

            .header-bar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; gap: 20px; }
            .logo-section { display: flex; align-items: center; gap: 15px; min-width: 300px; }

            .search-group { position: relative; flex-grow: 1; max-width: 400px; }
            .search-icon { position: absolute; left: 10px; top: 10px; color: #888; }
            #searchInput { padding: 10px 10px 10px 35px; border: 1px solid #ddd; border-radius: 4px; width: 100%; font-size: 0.9em; }

            .scan-group { display: flex; align-items: center; gap: 15px; min-width: 300px; justify-content: flex-end; }
            .last-refresh { font-size: 0.8em; color: #666; white-space: nowrap; }

            .btn-scan { background-color: #ce1126; color: white; border: none; padding: 10px 20px; border-radius: 4px; cursor: pointer; font-weight: bold; transition: background 0.2s; }
            .btn-scan:hover { background-color: #a00d1d; }
            .btn-scan:disabled { background-color: #ccc; }

            .stats-bar {
                display: flex;
                gap: 15px;
                align-items: center;
                margin: 0 0 12px 0;
                padding: 10px 12px;
                border: 1px solid #eee;
                border-radius: 6px;
                background: #fafafa;
                font-size: 0.9em;
            }
            .stat-item { color: #333; }
            .stat-online { color: #28a745; font-weight: 600; }
            .stat-offline { color: #999; font-weight: 600; }

            table { border-collapse: collapse; width: 100%; margin-top: 0px; }
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
`)

	// 2) Geräte-Liste kopieren + Statistik zählen (kurzer Lock)
	inventoryMutex.Lock()
	var devices []*IPC
	onlineCount := 0
	offlineCount := 0
	knownCount := 0

	for _, dev := range inventory {
		devices = append(devices, dev)

		if dev.IsReachable {
			onlineCount++
		} else {
			offlineCount++
		}

		// "Erkannt" = hat mindestens ein Merkmal (MAC / Hostname / AMS)
		if dev.MACAddress != "" || dev.Hostname != "" || dev.AmsNetID != "" {
			knownCount++
		}
	}
	inventoryMutex.Unlock()

	// 3) Statistik-Bar (jetzt ist sie WIRKLICH oberhalb der Tabelle)
	fmt.Fprintf(w, `
            <div class="stats-bar">
                <div class="stat-item">Erkannt: <strong>%d</strong></div>
                <div class="stat-item stat-online">Online: <strong>%d</strong></div>
                <div class="stat-item stat-offline">Offline: <strong>%d</strong></div>
            </div>
`, knownCount, onlineCount, offlineCount)

	// 4) Tabelle starten
	fmt.Fprint(w, `
            <table id="deviceTable">
                <thead>
                    <tr>
                        <th>Status</th><th>IP-Adresse</th><th>Hostname</th><th>Büro</th>
                        <th>MAC-Adresse</th><th>OS Version</th><th>AMS Net-ID</th>
                        <th>TwinCAT</th><th>Runtime</th><th>Zuletzt online</th>
                    </tr>
                </thead>
                <tbody>
`)

	// 5) Sortieren (ohne Lock)
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].IsReachable != devices[j].IsReachable {
			return devices[i].IsReachable
		}
		ipI := net.ParseIP(devices[i].IP).To4()
		ipJ := net.ParseIP(devices[j].IP).To4()
		return bytes.Compare(ipI, ipJ) < 0
	})

	// 6) Rows rendern
	for _, device := range devices {
		var lastSeenStr string
		if device.IsReachable {
			lastSeenStr = "<span style='color:#28a745;font-weight:bold;'>Jetzt</span>"
		} else if !device.LastSeenOnline.IsZero() {
			lastSeenStr = device.LastSeenOnline.Format("02.01.2006 15:04:05")
		} else {
			lastSeenStr = "-"
		}

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
                <td>%s</td>
            </tr>`,
			statusClass, statusText, device.IP, device.Hostname,
			device.Office, device.MACAddress, device.OSVersion,
			device.AmsNetID, device.TwinCATVersion, device.RuntimeStatus, lastSeenStr)
	}

	// 7) HTML schließen
	fmt.Fprint(w, `
                </tbody>
            </table>
        </div>
    </body>
    </html>
`)
}

// --- 7. MAIN ---

func main() {
	//Snapshot laden
	if err := loadSnapshot(); err != nil {
		fmt.Println("Snapshot load error:", err)
	}
	go runDiscovery()
	go runDiscovery()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/", handleDashboard)
	http.HandleFunc("/trigger-scan", handleTriggerScan)

	fmt.Println("-----------------------------------------------")
	const port = 18080

	fmt.Println("Beckhoff Inventar-Server Online")
	fmt.Printf("Lokal: http://localhost:%d\n", port)

	if ip, _, err := getLocalLabIPv4(); err == nil {
		fmt.Printf("Netz:  http://%s:%d\n", ip.String(), port)
	} else {
		fmt.Println("Netz:  (keine 172.17.76.x Adresse gefunden)")
	}

	fmt.Println("-----------------------------------------------")

	if err := http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), nil); err != nil {
		fmt.Println("ListenAndServe error:", err)
	}
}
