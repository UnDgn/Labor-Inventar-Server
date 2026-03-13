package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"sort"
	"text/template"
	"time"
)

// DashboardStats enthält die Kennzahlen, die oberhalb der Tabelle angezeigt werden.
type DashboardStats struct {
	Online  int // Anzahl aktuell erreichbarer Geräte
	Offline int // Anzahl aktuell nicht erreichbarer Geräte
	Known   int // Anzahl "erkannter" Geräte mit mindestens einem Merkmal
}

// DashboardModel ist das komplette ViewModel für das Dashboard.
type DashboardModel struct {
	LastUpdateStr string // Zeitstempel für die Header-Anzeige "Refreshed"
	Devices       []*IPC // sortierte Geräteliste für die Tabelle
	Stats         DashboardStats
}

//
// ------------------------------------------------------------
// Hilfsfunktion für relative Zeitangaben
// ------------------------------------------------------------
//

// formatRelativeTime formatiert einen Zeitstempel relativ zu "jetzt".
//
// Beispiele:
// - gerade eben
// - 5 min
// - 2 h
// - 3 d
// - 1 w
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}

	diff := time.Since(t)

	if diff < time.Minute {
		return "gerade eben"
	}
	if diff < time.Hour {
		return fmt.Sprintf("%d min", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%d h", int(diff.Hours()))
	}
	if diff < 7*24*time.Hour {
		return fmt.Sprintf("%d d", int(diff.Hours()/24))
	}

	return fmt.Sprintf("%d w", int(diff.Hours()/24/7))
}

//
// ------------------------------------------------------------
// Dashboard-Datenmodell aufbauen
// ------------------------------------------------------------
//

// buildDashboardModel baut das Datenmodell für die HTML-Ansicht auf.
//
// Ablauf:
// 1. Geräte aus dem Inventory unter Lock kopieren
// 2. Statistik berechnen
// 3. Geräte für die Anzeige sortieren
func buildDashboardModel() DashboardModel {
	lastUpdateStr := time.Now().Format("15:04:05")

	inventoryMutex.Lock()

	var devices []*IPC
	stats := DashboardStats{}

	for _, dev := range inventory {
		devices = append(devices, dev)

		if dev.IsReachable {
			stats.Online++
		} else {
			stats.Offline++
		}

		// Ein Gerät gilt als "erkannt", wenn mindestens ein Identitätsmerkmal bekannt ist.
		if dev.MACAddress != "" || dev.Hostname != "" || dev.AmsNetID != "" {
			stats.Known++
		}
	}

	inventoryMutex.Unlock()

	// Sortierung:
	// 1. Online-Geräte zuerst
	// 2. innerhalb dessen nach IP-Adresse aufsteigend
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].IsReachable != devices[j].IsReachable {
			return devices[i].IsReachable
		}

		ipI := net.ParseIP(devices[i].IP).To4()
		ipJ := net.ParseIP(devices[j].IP).To4()

		return bytes.Compare(ipI, ipJ) < 0
	})

	return DashboardModel{
		LastUpdateStr: lastUpdateStr,
		Devices:       devices,
		Stats:         stats,
	}
}

//
// ------------------------------------------------------------
// Dashboard-HTML rendern
// ------------------------------------------------------------
//

// renderDashboard schreibt die komplette HTML-Seite direkt in den ResponseWriter.
//
// Die Datei rendert aktuell bewusst "roh" per fmt.Fprint / fmt.Fprintf,
// also ohne separates Template-File.
// Dadurch liegt die komplette Dashboard-Ansicht in Go-Code.
func renderDashboard(w http.ResponseWriter, m DashboardModel) {
	//
	// --------------------------------------------------------
	// 1) HTML-Kopf, CSS, JS und obere Header-Bar
	// --------------------------------------------------------
	//
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

			th[draggable="true"] { cursor: grab; user-select: none; }
			th.dragging { opacity: 0.5; }
			th.drag-over { outline: 2px dashed rgba(255,255,255,0.7); outline-offset: -6px; }

            /* Spaltenbreiten stabil machen */
            #deviceTable { table-layout: fixed; width: 100%; }
            #deviceTable th, #deviceTable td { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

            /* Resizer-Griff */
            th { position: relative; }

			.btn-reset {
                background-color: #eee;
                color: #333;
                border: 1px solid #ddd;
                padding: 10px 14px;
                border-radius: 4px;
                cursor: pointer;
                font-weight: 600;
            }
            .btn-reset:hover { background-color: #e2e2e2; }

            .col-resizer {
                position: absolute;
                top: 0;
                right: 0;
                width: 10px;
                height: 100%;
                cursor: col-resize;
                z-index: 9999;
                background: rgba(0,0,0,0.0);
                pointer-events: auto;
            }

            .col-resizer:hover {
                background: rgba(255,255,255,0.35);
            }

            .ip-cell {
                display: flex;
                align-items: top;
                gap: 6px;
                min-height: 34px;
            }

            .rdp-icon {
                text-decoration: none;
                font-size: 1.05em;
                color: #0078d4;
            }

            .rdp-icon:hover {
                transform: scale(1.15);
            }

            .rdp-icon.disabled {
                opacity: 0.25;
                cursor: default;
            }

            /* kompakte Standard-Spaltenbreiten */
            th[data-col="fav"],
            td[data-col="fav"] {
                width: 36px;
                min-width: 36px;
                max-width: 36px;
                padding-left: 6px;
                padding-right: 6px;
                text-align: center;
            }

            th[data-col="status"], td[data-col="status"] {
                width: 80px;
                min-width: 80px;
                max-width: 80px;
                padding-right: 4px;
            }

            th[data-col="ip"], td[data-col="ip"] {
                width: 150px;
                min-width: 150px;
                max-width: 170px;
            }

            th[data-col="office"],
            td[data-col="office"] {
                width: 75px;
                min-width: 65px;
                max-width: 100px;
            }

            .office-select {
                width: 100%;
                font-size: 0.85em;
                padding: 4px 6px;
                border: 1px solid #ddd;
                border-radius: 4px;
                background: white;
            }

            th[data-col="comment"],
            td[data-col="comment"] {
                width: 260px;
                min-width: 200px;
                max-width: 280px;
            }

            .comment-input {
                width: 100%;
                height: 34px;
                min-height: 34px;
                resize: vertical;
                overflow-y: auto;
                font-size: 0.95em;
                line-height: 1.3;
                padding: 6px 8px;
                border: 1px solid #ddd;
                border-radius: 4px;
                box-sizing: border-box;
                font-family: "Segoe UI", sans-serif;
            }

            th[data-col="twincat"], td[data-col="twincat"] {
                width: 95px;
                min-width: 95px;
                max-width: 110px;
            }

            th[data-col="lastonline"], td[data-col="lastonline"] {
                width: 140px;
                min-width: 170px;
                max-width: 250px;
            }

            #deviceTable { table-layout: fixed; }
            #deviceTable th, #deviceTable td { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

            /* Favoriten */
            .fav-btn {
                background: transparent;
                border: 0;
                cursor: pointer;
                font-size: 16px;
                line-height: 1;
                padding: 2px 4px;
            }

            .fav-btn.is-fav {
                color: #ce1126;
                font-weight: bold;
            }

            .fav-btn:hover { transform: scale(1.2); }

            .fav-btn.disabled {
                opacity: 0.25;
                cursor: default;
                pointer-events: none;
            }

            /* Farbgebung TwinCAT-/Runtime-State */
            .runtime-run {
                color: #28a745;
                font-weight: 600;
            }

            .runtime-stop {
                color: #c62828;
                font-weight: 600;
            }

            .runtime-config {
                color: #1565c0;
                font-weight: 600;
            }

            .runtime-offline {
                color: #999;
                font-weight: 600;
            }
            .active-filter {
                background-color: #ce1126;
                color: white;
                border-color: #ce1126;
            }

            /* Statistikleiste */
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
            td { padding: 12px 10px; border-bottom: 1px solid #eee; font-size: 0.85em; vertical-align: top; }
            .status-online { color: #28a745; font-weight: bold; }
            .status-offline { color: #ccc; }
        </style>
        <script>
           function filterTable() {
                const filter = document.getElementById("searchInput").value.toUpperCase();
                const rows = document.querySelectorAll("#deviceTable tbody tr");

                rows.forEach(row => {
                    const office = (row.dataset.office || "").toUpperCase();

                    // Zeilentext ohne die Select-Optionen der Bürospalte
                    let rowText = "";
                    row.querySelectorAll("td").forEach(td => {
                        if (td.dataset.col === "office") {
                            const select = td.querySelector("select");
                            if (select) {
                                rowText += " " + (select.value || "");
                            }
                        } else {
                            rowText += " " + td.textContent;
                        }
                    });

                    rowText = rowText.toUpperCase();

                    row.style.display = rowText.includes(filter) || office.includes(filter) ? "" : "none";
                });
            }
             function startScan() {
                const btn = document.getElementById("scanBtn");
                btn.disabled = true;
                btn.innerText = "Scanning...";
            
                fetch('/trigger-scan').then(() => {
                    waitForScanToFinish();
                });
            }
            function waitForScanToFinish() {
                const interval = setInterval(() => {
                    fetch('/api/scan-status')
                        .then(r => r.json())
                        .then(data => {
                            if (!data.isScanning) {
                                clearInterval(interval);
                                location.reload();
                            }
                        })
                        .catch(() => {
                            clearInterval(interval);
                        });
                }, 1000);
            }
        </script>
    </head>
    <body>
        <div class="container">
            <div class="header-bar">
                <div class="logo-section">
                    <img src="/static/logo.png" alt="Beckhoff" style="height: 40px;">
                    <h1 style="margin: 0; font-size: 1.5em; font-weight: 300; display: flex; align-items: center; gap: 10px;">
                        Testnetz 
                        <a href="mailto:DoganY@beckhoff.com?subject=Feedback Testnetz Tool" 
                           style="text-decoration: none; font-size: 0.4em; font-weight: bold; color: #ce1126; border: 1px solid #ce1126; padding: 2px 6px; border-radius: 4px; cursor: pointer;"
                           title="Bug gefunden? Hier klicken!">
                           BETA
                        </a>
                    </h1>
                </div>

                <div class="search-group">
                    <span class="search-icon">🔍</span>
                    <input type="text" id="searchInput" onkeyup="filterTable()" placeholder="Gerät suchen (IP, Name, MAC)...">
                </div>

                <div class="scan-group">
                    <div class="last-refresh">Refreshed: <strong>`+m.LastUpdateStr+`</strong></div>
                    <button id="scanBtn" class="btn-scan" onclick="startScan()">Scan</button>
                    <button class="btn-reset" onclick="resetColumns()" title="Spaltenlayout zurücksetzen">Reset</button>
                    <button id="favFilterBtn" class="btn-reset" onclick="toggleFavoriteFilter()" title="Nur Favoriten anzeigen">Nur Favoriten</button>
                </div>
            </div>
`)

	//
	// --------------------------------------------------------
	// 2) Statistik-Bar oberhalb der Tabelle
	// --------------------------------------------------------
	//
	fmt.Fprintf(w, `
            <div class="stats-bar">
                <div class="stat-item">Erkannt: <strong>%d</strong></div>
                <div class="stat-item stat-online">Online: <strong>%d</strong></div>
                <div class="stat-item stat-offline">Offline: <strong>%d</strong></div>
            </div>
`,
		m.Stats.Known,
		m.Stats.Online,
		m.Stats.Offline,
	)

	//
	// --------------------------------------------------------
	// 3) Tabellenkopf rendern
	// --------------------------------------------------------
	//
	fmt.Fprint(w, `
<table id="deviceTable">
  <colgroup>
    <col data-col="fav">
    <col data-col="status">
    <col data-col="ip">
    <col data-col="hostname">
    <col data-col="office">
    <col data-col="comment">
    <col data-col="mac">
    <col data-col="os">
    <col data-col="ams">
    <col data-col="twincat">
    <col data-col="runtime">
    <col data-col="lastonline">
  </colgroup>
  <thead>
    <tr id="headerRow">
      <th data-col="fav" draggable="true">★<span class="col-resizer"></span></th>
      <th data-col="status" draggable="true">Status<span class="col-resizer"></span></th>
      <th data-col="ip" draggable="true">IP-Adresse<span class="col-resizer"></span></th>
      <th data-col="hostname" draggable="true">Hostname<span class="col-resizer"></span></th>
      <th data-col="office" draggable="true">Büro<span class="col-resizer"></span></th>
      <th data-col="comment" draggable="true">Kommentar<span class="col-resizer"></span></th>
      <th data-col="mac" draggable="true">MAC-Adresse<span class="col-resizer"></span></th>
      <th data-col="os" draggable="true">OS Version<span class="col-resizer"></span></th>
      <th data-col="ams" draggable="true">AMS Net-ID<span class="col-resizer"></span></th>
      <th data-col="twincat" draggable="true">TwinCAT<span class="col-resizer"></span></th>
      <th data-col="runtime" draggable="true">TC State<span class="col-resizer"></span></th>
      <th data-col="lastonline" draggable="true">Zuletzt online<span class="col-resizer"></span></th>
    </tr>
  </thead>
  <tbody>
`)

	//
	// --------------------------------------------------------
	// 4) Tabellenzeilen pro Gerät rendern
	// --------------------------------------------------------
	//
	for _, device := range m.Devices {
		//
		// --------------------------------------------
		// Letzte Online-Zeit aufbereiten
		// --------------------------------------------
		//
		var lastSeenStr string

		if device.IsReachable {
			lastSeenStr = "<span style='color:#28a745;font-weight:bold;'>Jetzt</span>"
		} else if !device.LastSeenOnline.IsZero() {
			relative := formatRelativeTime(device.LastSeenOnline)
			absolute := device.LastSeenOnline.Format("02.01.2006 15:04:05")

			lastSeenStr = fmt.Sprintf(`
        <div style="line-height:1.2">
            <div>%s</div>
            <div style="font-size:0.75em;color:#888;">(%s)</div>
        </div>
    `, relative, absolute)
		} else {
			lastSeenStr = "-"
		}

		//
		// --------------------------------------------
		// Online/Offline-Anzeige
		// --------------------------------------------
		//
		statusClass, statusText := "status-offline", "Offline"
		if device.IsReachable {
			statusClass, statusText = "status-online", "Online"
		}

		//
		// --------------------------------------------
		// RDP-Button
		// --------------------------------------------
		//
		// Nur bei erreichbaren Geräten aktiv
		rdpButton := `<span class="rdp-icon disabled">🖥</span>`
		if device.IsReachable {
			rdpButton = fmt.Sprintf(
				`<a href="beckhoff-rdp://%s" class="rdp-icon" title="RDP öffnen">🖥</a>`,
				device.IP,
			)
		}

		//
		// --------------------------------------------
		// Favoriten-Button
		// --------------------------------------------
		//
		favKey := device.MACAddress
		if favKey == "" {
			favKey = device.IP
		}

		favCell := fmt.Sprintf(
			`<button class="fav-btn" data-fav="%s" title="Favorit umschalten">☆</button>`,
			favKey,
		)

		//
		// --------------------------------------------
		// Büro-Dropdown auf Basis der zentralen validOffices-Liste
		// --------------------------------------------
		//
		officeOptions := append([]string{""}, validOffices...)

		officeSelect := `<select class="office-select" data-mac="` + device.MACAddress + `">`

		for _, office := range officeOptions {
			selected := ""
			if device.Office == office {
				selected = ` selected`
			}

			label := office
			if office == "" {
				label = "-"
			}

			officeSelect += `<option value="` + office + `"` + selected + `>` + label + `</option>`
		}

		officeSelect += `</select>`

		// Ohne MAC kann keine Zuordnung gespeichert werden → Dropdown deaktivieren
		if device.MACAddress == "" {
			officeSelect = `<select class="office-select" disabled><option>-</option></select>`
		}

		//
		// --------------------------------------------
		// Kommentar-Feld
		// --------------------------------------------
		//
		commentInput := `<textarea class="comment-input" data-mac="` + device.MACAddress +
			`" title="` + template.HTMLEscapeString(device.Comment) +
			`" placeholder="Kommentar...">` +
			template.HTMLEscapeString(device.Comment) + `</textarea>`

		if device.MACAddress == "" {
			commentInput = `<textarea class="comment-input" placeholder="Kommentar..." disabled></textarea>`
		}

		//
		// --------------------------------------------
		// Runtime-/TwinCAT-State farblich markieren
		// --------------------------------------------
		//
		runtimeClass := ""

		if !device.IsReachable && device.RuntimeStatus != "" {
			runtimeClass = "runtime-offline"
		} else {
			switch device.RuntimeStatus {
			case "RUN":
				runtimeClass = "runtime-run"
			case "STOP":
				runtimeClass = "runtime-stop"
			case "CONFIG":
				runtimeClass = "runtime-config"
			}
		}

		//
		// --------------------------------------------
		// Tabellenzeile rendern
		// --------------------------------------------
		//
		fmt.Fprintf(w, `<tr data-office="%s">
  <td data-col="fav" class="fav-cell">%s</td>
  <td data-col="status" class="%s">%s</td>
  <td data-col="ip"><div class="ip-cell">%s<strong>%s</strong></div></td>
  <td data-col="hostname">%s</td>
  <td data-col="office">%s</td>
  <td data-col="comment">%s</td>
  <td data-col="mac" class="mac-font">%s</td>
  <td data-col="os">%s</td>
  <td data-col="ams">%s</td>
  <td data-col="twincat">%s</td>
  <td data-col="runtime"><span class="%s">%s</span></td>
  <td data-col="lastonline">%s</td>
</tr>`,
			device.Office,
			favCell,
			statusClass, statusText,
			rdpButton, device.IP,
			device.Hostname,
			officeSelect,
			commentInput,
			device.MACAddress,
			device.OSVersion,
			device.AmsNetID,
			device.TwinCATVersion,
			runtimeClass, device.RuntimeStatus,
			lastSeenStr,
		)
	}

	//
	// --------------------------------------------------------
	// 5) HTML sauber schließen
	// --------------------------------------------------------
	//
	fmt.Fprintf(w, `
                </tbody>
            </table>
        </div>

        <script src="/static/app.js?v=%d"></script>
    </body>
    </html>
`, time.Now().Unix())
}
