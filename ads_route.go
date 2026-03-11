package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"time"
)

// ------------------------------------------------------------
// Ergebnisstruktur für ADS-/TwinCAT-State-Abfragen
// ------------------------------------------------------------
//
// PlcStateResult transportiert das Ergebnis eines ReadState-Aufrufs.
//
// Status:
//
//	z. B. RUN / STOP / CONFIG / "State X / Dev Y"
//
// Err:
//
//	leer bei Erfolg, sonst Diagnose-/Fehlertext
//
// RuntimePort:
//
//	der verwendete AMS-Port, auf dem der Status erfolgreich gelesen wurde
type PlcStateResult struct {
	Status      string
	Err         string
	RuntimePort uint16
}

// ------------------------------------------------------------
// Relevante ADS-/TwinCAT-Ports
// ------------------------------------------------------------
//
// 48899  = ADS UDP (Broadcast / AddRoute)
// 801    = TwinCAT 2 Runtime 1
// 851    = TwinCAT 3 PLC Runtime 1
// 10000  = TwinCAT System Service
const (
	routeUdpPort48899    = 48899
	routeTc2Runtime801   = uint16(801)
	routeTc3Runtime851   = uint16(851)
	routeTcSystemService = uint16(10000)
)

// ------------------------------------------------------------
// Byte-Segmente für ADS UDP AddRoute
// ------------------------------------------------------------
//
// Diese Konstanten entsprechen dem Beckhoff-UDP-Protokoll
// zum Anlegen einer ADS-Route.
var (
	routeSegHeader          = []byte{0x03, 0x66, 0x14, 0x71}
	routeSegEnd             = []byte{0, 0, 0, 0}
	routeSegPort10000       = []byte{0x10, 0x27}    // Port 10000 little-endian
	routeSegReqAddRoute     = []byte{6, 0, 0, 0}    // REQUEST_ADDROUTE
	routeSegRespAddRoute    = []byte{6, 0, 0, 0x80} // RESPONSE_ADDROUTE
	routeSegRouteTypeStatic = []byte{5, 0, 0, 0}    // ROUTETYPE_STATIC

	routeSegRouteNameL = []byte{0x0c, 0, 0, 0} // ROUTENAME_L
	routeSegUserNameL  = []byte{0x0d, 0, 0, 0} // USERNAME_L
	routeSegPasswordL  = []byte{2, 0, 0, 0}    // PASSWORD_L
	routeSegLocalHostL = []byte{5, 0, 0, 0}    // LOCALHOST_L
	routeSegAmsNetIdL  = []byte{7, 0, 6, 0}    // AMSNETID_L
)

// ------------------------------------------------------------
// Hilfsfunktion: ASCII + Nullterminierung
// ------------------------------------------------------------
//
// Beckhoff erwartet viele Stringfelder im UDP-AddRoute-Paket
// als ASCII-Zeichenfolge mit abschließendem '\0'.
func asciiZ(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, []byte(s))
	b[len(b)-1] = 0

	return b
}

// ------------------------------------------------------------
// UDP AddRoute-Paket bauen
// ------------------------------------------------------------
//
// buildAddRoutePacket erzeugt das Beckhoff-UDP-Telegramm
// zum Anlegen einer statischen ADS-Route auf dem Zielgerät.
//
// Verwendet werden:
//
// - lokale AMS Net ID
// - Routenname
// - Benutzername
// - Passwort
// - lokaler Hostname bzw. IP
//
// Die *_L-Segmente enthalten jeweils die Länge des nachfolgenden Feldes.
func buildAddRoutePacket(localAmsNetID [6]byte, routeName, user, pass, localHostNameOrIP string) []byte {
	routeNameB := asciiZ(routeName)
	userB := asciiZ(user)
	passB := asciiZ(pass)
	localHostB := asciiZ(localHostNameOrIP)

	// Länge der variablen Felder in die Beckhoff-Segmente einsetzen
	routeNameL := append([]byte(nil), routeSegRouteNameL...)
	routeNameL[2] = byte(len(routeNameB))

	userL := append([]byte(nil), routeSegUserNameL...)
	userL[2] = byte(len(userB))

	passL := append([]byte(nil), routeSegPasswordL...)
	passL[2] = byte(len(passB))

	hostL := append([]byte(nil), routeSegLocalHostL...)
	hostL[2] = byte(len(localHostB))

	var out []byte

	out = append(out, routeSegHeader...)
	out = append(out, routeSegEnd...)
	out = append(out, routeSegReqAddRoute...)
	out = append(out, localAmsNetID[:]...)
	out = append(out, routeSegPort10000...)
	out = append(out, routeSegRouteTypeStatic...)

	out = append(out, routeNameL...)
	out = append(out, routeNameB...)

	out = append(out, routeSegAmsNetIdL...)
	out = append(out, localAmsNetID[:]...)

	out = append(out, userL...)
	out = append(out, userB...)

	out = append(out, passL...)
	out = append(out, passB...)

	out = append(out, hostL...)
	out = append(out, localHostB...)

	return out
}

// ------------------------------------------------------------
// AddRoute-Antwort prüfen
// ------------------------------------------------------------
//
// parseAddRouteAck prüft, ob ein empfangenes UDP-Paket
// eine gültige Beckhoff-AddRoute-Antwort ist
// und ob der ACK-Status "erfolgreich" zurückmeldet.
func parseAddRouteAck(pkt []byte) bool {
	minLen := 4 + 4 + 4 + 6 + 2 + 4 + 4 + 4
	if len(pkt) < minLen {
		return false
	}

	if !bytes.Equal(pkt[0:4], routeSegHeader) {
		return false
	}
	if !bytes.Equal(pkt[4:8], routeSegEnd) {
		return false
	}
	if !bytes.Equal(pkt[8:12], routeSegRespAddRoute) {
		return false
	}

	// ACK-Feld liegt hinter:
	// Header + End + Response + AMSNetID + Port + End + End
	shift := 4 + 4 + 4 + 6 + 2 + 4 + 4
	ack := pkt[shift : shift+4]

	return ack[0] == 0 && ack[1] == 0
}

// ------------------------------------------------------------
// ADS-Route per UDP sicherstellen
// ------------------------------------------------------------
//
// EnsureAdsRouteUDP versucht auf dem Zielgerät eine ADS-Route
// zum lokalen Rechner anzulegen.
//
// Ablauf:
// 1. lokale AMS Net ID aus lokaler IPv4 bilden
// 2. Default-Werte für Route/User/Pass setzen
// 3. UDP AddRoute-Paket bauen
// 4. an Zielgerät senden
// 5. auf gültige ACK-Antwort warten
//
// Wichtiger Praxispunkt:
// Das UDP-Socket wird "unconnected" geöffnet,
// damit Antworten auch von anderem Source-Port akzeptiert werden können.
func EnsureAdsRouteUDP(
	ctx context.Context,
	localIP net.IP,
	remoteIP net.IP,
	routeName string,
	user string,
	pass string,
	udpPort int,
) error {
	ip4 := localIP.To4()
	if ip4 == nil {
		return fmt.Errorf("localIP is not IPv4: %v", localIP)
	}

	rip4 := remoteIP.To4()
	if rip4 == nil {
		return fmt.Errorf("remoteIP is not IPv4: %v", remoteIP)
	}

	// Lokale AMS Net ID = <lokale IP>.1.1
	var localAms [6]byte
	copy(localAms[0:4], ip4)
	localAms[4] = 1
	localAms[5] = 1

	// Default-Werte ergänzen
	if routeName == "" {
		routeName, _ = os.Hostname()
		if routeName == "" {
			routeName = "GoInventory"
		}
	}

	if user == "" {
		user = "Administrator"
	}
	if pass == "" {
		pass = "1"
	}

	// Als LOCALHOST im Paket bewusst die lokale IPv4 verwenden
	// statt des Hostnamens, damit das Ziel den Rückweg sauber kennt.
	localHostNameOrIP := ip4.String()

	req := buildAddRoutePacket(localAms, routeName, user, pass, localHostNameOrIP)

	// UDP-Socket auf lokaler Testnetz-IP öffnen
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip4, Port: 0})
	if err != nil {
		return err
	}
	defer conn.Close()

	deadline := time.Now().Add(2500 * time.Millisecond)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}

	_ = conn.SetDeadline(deadline)

	// AddRoute-Request senden
	_, err = conn.WriteToUDP(req, &net.UDPAddr{IP: rip4, Port: udpPort})
	if err != nil {
		return err
	}

	// Auf ACK-Antwort warten
	buf := make([]byte, 2048)

	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err // Timeout oder echtes Socketproblem
		}

		// Nur Antworten des Zielhosts akzeptieren
		if !raddr.IP.Equal(rip4) {
			continue
		}

		if parseAddRouteAck(buf[:n]) {
			return nil
		}

		// Andere Pakete ignorieren, bis Deadline erreicht ist
	}
}

// ------------------------------------------------------------
// Kandidatenliste für PLC-Runtime-Ports
// ------------------------------------------------------------
//
// Diese Funktion stammt aus der früheren PLC-State-Strategie.
//
// Idee:
// - zuerst bekannten erfolgreichen Port testen
// - danach je nach TwinCAT-Version typische Runtime-Ports probieren
//
// Aktueller Stand:
// Für den derzeitigen TwinCAT-System-State über Port 10000
// ist die Funktion nicht aktiv nötig,
// bleibt aber als vorbereitete Helper-Logik im Projekt.
func candidateRuntimePorts(tcVersion string, preferred uint16) []uint16 {
	var ports []uint16

	add := func(p uint16) {
		if p == 0 {
			return
		}
		for _, x := range ports {
			if x == p {
				return
			}
		}
		ports = append(ports, p)
	}

	// zuerst bekannten erfolgreichen Port
	add(preferred)

	// dann passend zur Version priorisieren
	if len(tcVersion) > 0 && tcVersion[0] == '3' {
		for _, p := range []uint16{851, 852, 853, 854} {
			add(p)
		}
		for _, p := range []uint16{801, 811, 821, 831} {
			add(p)
		}
	} else if len(tcVersion) > 0 && tcVersion[0] == '2' {
		for _, p := range []uint16{801, 811, 821, 831} {
			add(p)
		}
		for _, p := range []uint16{851, 852, 853, 854} {
			add(p)
		}
	} else {
		for _, p := range []uint16{851, 852, 853, 854, 801, 811, 821, 831} {
			add(p)
		}
	}

	return ports
}

// ------------------------------------------------------------
// TwinCAT System State lesen
// ------------------------------------------------------------
//
// readTwinCATSystemState liest den TwinCAT-Systemzustand
// über den Beckhoff System Service Port 10000.
//
// Der eigentliche AMS/TCP-ReadState-Aufruf wird in readAdsStateRaw()
// ausgeführt. Diese Funktion übersetzt nur den numerischen ADS-State
// in einen lesbaren Status-String.
func readTwinCATSystemState(remoteIP net.IP, remoteAmsNetID string, localIP net.IP) PlcStateResult {
	resp, err := readAdsStateRaw(remoteIP, remoteAmsNetID, routeTcSystemService, localIP, 4*time.Second)
	if err != nil {
		return PlcStateResult{
			Status: "no Info",
			Err:    fmt.Sprintf("ReadState(port %d): %s", routeTcSystemService, err.Error()),
		}
	}

	switch resp.AdsState {
	case 5:
		return PlcStateResult{
			Status:      "RUN",
			Err:         "",
			RuntimePort: routeTcSystemService,
		}
	case 6:
		return PlcStateResult{
			Status:      "STOP",
			Err:         "",
			RuntimePort: routeTcSystemService,
		}
	case 15:
		return PlcStateResult{
			Status:      "CONFIG",
			Err:         "",
			RuntimePort: routeTcSystemService,
		}
	default:
		return PlcStateResult{
			Status:      fmt.Sprintf("State %d / Dev %d", resp.AdsState, resp.DeviceState),
			Err:         "",
			RuntimePort: routeTcSystemService,
		}
	}
}

// ------------------------------------------------------------
// Beliebigen ADS-State über frei wählbaren AMS-Port lesen
// ------------------------------------------------------------
//
// readAdsState ist die allgemeinere Variante zu readTwinCATSystemState().
//
// Sie kann prinzipiell auch für PLC-Runtime-Ports wie 801/851
// oder weitere ADS-Ziele verwendet werden.
func readAdsState(remoteIP net.IP, remoteAmsNetID string, amsPort uint16, localIP net.IP) PlcStateResult {
	resp, err := readAdsStateRaw(remoteIP, remoteAmsNetID, amsPort, localIP, 4*time.Second)
	if err != nil {
		return PlcStateResult{
			Status: "no Info",
			Err:    fmt.Sprintf("ReadState(port %d): %s", amsPort, err.Error()),
		}
	}

	switch resp.AdsState {
	case 5:
		return PlcStateResult{
			Status:      "RUN",
			Err:         "",
			RuntimePort: amsPort,
		}
	case 6:
		return PlcStateResult{
			Status:      "STOP",
			Err:         "",
			RuntimePort: amsPort,
		}
	case 15:
		return PlcStateResult{
			Status:      "CONFIG",
			Err:         "",
			RuntimePort: amsPort,
		}
	default:
		return PlcStateResult{
			Status:      fmt.Sprintf("State %d / Dev %d", resp.AdsState, resp.DeviceState),
			Err:         "",
			RuntimePort: amsPort,
		}
	}
}

// ------------------------------------------------------------
// TwinCAT-State eines Remote-Geräts lesen
// ------------------------------------------------------------
//
// TryReadTCState ist die zentrale High-Level-Funktion für scanner.go.
//
// Ablauf:
// 1. ADS-Route zum Zielgerät sicherstellen
// 2. TwinCAT System State über Port 10000 lesen
//
// Falls die Route nicht gesetzt werden kann,
// wird direkt ein Fehler zurückgegeben.
func TryReadTCState(ctx context.Context, localIP net.IP, remoteIP net.IP, remoteAmsNetID string) PlcStateResult {
	// 1) Route anlegen
	{
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		hostName, _ := os.Hostname()

		if err := EnsureAdsRouteUDP(
			cctx,
			localIP,
			remoteIP,
			hostName,
			"Administrator",
			"1",
			routeUdpPort48899,
		); err != nil {
			fmt.Println("Route FAILED for", remoteIP.String(), "->", remoteAmsNetID, "err:", err)

			return PlcStateResult{
				Status: "no Info",
				Err:    "route: " + err.Error(),
			}
		}

		fmt.Println("Route OK for", remoteIP.String(), "->", remoteAmsNetID)
	}

	// 2) TwinCAT System State über Port 10000 lesen
	return readTwinCATSystemState(remoteIP, remoteAmsNetID, localIP)
}
