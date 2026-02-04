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
	const port = 18080

	fmt.Println("Beckhoff Inventar-Server Online")
	fmt.Printf("Lokal: http://localhost:%d\n", port)
	fmt.Printf("Netz:  http://172.17.76.43:%d\n", port)
	fmt.Println("-----------------------------------------------")

	_ = http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), nil)
}
