package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// ------------------------------------------------------------
// Ergebnisstruktur der ADS-UDP-Discovery
// ------------------------------------------------------------
//
// RemotePlcInfo enthält alle Informationen, die aus einer
// Beckhoff-UDP-Discovery-Antwort extrahiert werden können.
type RemotePlcInfo struct {
	Name        string // Gerätename aus dem Discovery-Telegramm
	Address     net.IP // IP-Adresse des antwortenden Geräts
	AmsNetID    string // AMS Net ID des Geräts
	OsVersion   string // dekodierte OS-/Build-Information
	Fingerprint string // optionaler Fingerprint aus dem Tail
	Comment     string // optionaler Kommentar aus dem Telegramm
	TcVersion   AdsVersion
	IsRuntime   string // UDP-Hinweis: Runtime / Engineering / no Info
	Hostname    string // Hostname, falls aus dem Tail extrahierbar
}

// TwinCAT-Version, wie sie aus dem UDP-Telegramm gelesen wird.
//
// Beispiel:
// 3.1.4024
type AdsVersion struct {
	Version  uint8
	Revision uint8
	Build    int16
}

func (v AdsVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Version, v.Revision, v.Build)
}

// ------------------------------------------------------------
// UDP-Segmente / Protokollkonstanten
// ------------------------------------------------------------
//
// Diese Bytefolgen entsprechen dem Beckhoff ADS-UDP-Discovery-Protokoll.
var (
	udpSegHeader           = []byte{0x03, 0x66, 0x14, 0x71}
	udpSegEnd              = []byte{0, 0, 0, 0}
	udpSegRequestDiscover  = []byte{1, 0, 0, 0}
	udpSegResponseDiscover = []byte{1, 0, 0, 0x80}
	udpSegPort10000        = []byte{0x10, 0x27} // Port 10000 little-endian
	udpSegRouteTypeStatic  = []byte{5, 0, 0, 0}

	// TwinCAT Type Marker im Discovery-Paket
	udpSegTcatRuntime     = []byte{4, 0, 0x14, 1, 0x14, 1, 0, 0}
	udpSegTcatEngineering = []byte{4, 0, 0x94, 0, 0x94, 0, 0, 0}

	// feste Feldlängen im UDP-Telegramm
	udpLenNameLen    = 4
	udpLenOSVersion  = 12
	udpLenDescMarker = 4

	udpPort48899 = 48899
)

// ------------------------------------------------------------
// OS-Erkennungstabellen
// ------------------------------------------------------------
//
// Beckhoff liefert OS-Informationen als numerische IDs.
// Diese Maps übersetzen sie in lesbare Texte.
var udpOsIDs = map[uint16]string{
	0x0A00: "Windows",
	0x0700: "Win CE (7.0)",
	0x0602: "Win 8/8.1/10",
	0x0601: "Win 7",
	0x0600: "Win CE (6.0)",
	0x0500: "Win CE (5.0)",
	0x0501: "Win XP",
	0x0009: "RTOS",
}

var udpOsBuildIDs = map[uint16]string{
	0x5866: "11 (26200) 25H2",
	0xF465: "11 (26100) 24H2",
	0x6758: "11 (22631) 23H2",
	0x5D58: "11 (22621) 22H2",
	0x654A: "10 (19045) 22H2",
	0x644A: "10 (19044) 21H2",
	0x634A: "10 (19043) 21H1",
	0x624A: "10 (19042) 20H2",
	0x614A: "10 (19041) 2004",
	0x4447: "10 (18363) 1909",
	0xBA47: "10 (18362) 1903",
	0x6345: "10 (17763) 1809",
	0xEE42: "10 (17134) 1803",
	0xAB3F: "10 (16299) 1709",
	0xD73A: "10 (15063) 1703",
	0x3938: "10 (14393) 1607",
	0x5A29: "10 (10586) 1511",
	0x0028: "10 (10240) 1507",
}

// ------------------------------------------------------------
// Parser-Hilfsstruktur für UDP-Antworten
// ------------------------------------------------------------
//
// udpResponseResult kapselt den Rohbuffer einer Antwort
// plus aktuelle Parserposition.
type udpResponseResult struct {
	Buffer     []byte
	RemoteHost net.IP
	Shift      int
}

// nextChunk liest n Bytes ab aktueller Position.
//
// Parameter:
// - n: Anzahl Bytes
// - peek: wenn true, Position nicht verschieben
// - add: zusätzliche Bytes überspringen nach dem Lesen
func (rr *udpResponseResult) nextChunk(n int, peek bool, add int) []byte {
	if n < 0 {
		n = 0
	}

	if rr.Shift+n > len(rr.Buffer) {
		n = maxInt(0, len(rr.Buffer)-rr.Shift)
	}

	ch := rr.Buffer[rr.Shift : rr.Shift+n]

	if !peek {
		rr.Shift += n + add
	}

	return ch
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

//
// ------------------------------------------------------------
// Netz-Hilfsfunktionen
// ------------------------------------------------------------
//

// isInLabSubnetIP prüft, ob eine IPv4-Adresse im Testnetz 172.17.76.0/24 liegt.
func isInLabSubnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 172 && ip4[1] == 17 && ip4[2] == 76
}

// getLocalLabIPv4 sucht auf dem lokalen Rechner die IPv4-Adresse,
// die im Testnetz 172.17.76.0/24 liegt.
//
// Diese Adresse wird für:
// - UDP-Broadcast-Discovery
// - ADS-Route-Aufbau
// - spätere ADS-Kommunikation
// verwendet.
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

// broadcastAddr berechnet aus IP + Netzmaske die Broadcast-Adresse.
//
// Beispiel:
// 172.17.76.23 + 255.255.255.0 -> 172.17.76.255
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

// ------------------------------------------------------------
// UDP Discovery Request bauen
// ------------------------------------------------------------
//
// Der Discovery-Request enthält als lokale AMS-Adresse:
//
//	<lokale IPv4>.1.1
//
// und fragt per UDP-Broadcast alle Beckhoff-/TwinCAT-Geräte im Netz an.
func buildDiscoverRequest(localIP net.IP) []byte {
	ip4 := localIP.To4()

	// Standard AMS-Form: ip0.ip1.ip2.ip3.1.1
	ams := []byte{0, 0, 0, 0, 1, 1}
	if ip4 != nil {
		copy(ams[0:4], ip4)
	}

	var out []byte
	out = append(out, udpSegHeader...)
	out = append(out, udpSegEnd...)
	out = append(out, udpSegRequestDiscover...)
	out = append(out, ams...)
	out = append(out, udpSegPort10000...)
	out = append(out, udpSegEnd...)

	return out
}

// ------------------------------------------------------------
// UDP Discovery Antwort parsen
// ------------------------------------------------------------
//
// Diese Funktion interpretiert das proprietäre Beckhoff-UDP-Telegramm
// und extrahiert daraus:
//
// - AMS Net ID
// - Name
// - TwinCAT Typ
// - OS Version
// - Hostname
// - TwinCAT Version
// - optional Kommentar
//
// Die Logik ist eine funktionale Portierung der vorhandenen C#-Version.
func parseBroadcastSearchResponse(rr *udpResponseResult) RemotePlcInfo {
	dev := RemotePlcInfo{Address: rr.RemoteHost}

	// Minimallänge prüfen
	if len(rr.Buffer) < 12 {
		return dev
	}

	// Header prüfen
	if !bytes.Equal(rr.Buffer[0:4], udpSegHeader) {
		return dev
	}
	if !bytes.Equal(rr.Buffer[4:8], udpSegEnd) {
		return dev
	}
	if !bytes.Equal(rr.Buffer[8:12], udpSegResponseDiscover) {
		return dev
	}

	// Parserposition direkt hinter Header setzen
	rr.Shift = 4 + 4 + 4

	//
	// --------------------------------------------------------
	// AMS Net ID
	// --------------------------------------------------------
	//
	// Danach zusätzlich 2 Bytes Port + 4 Bytes RouteType überspringen.
	ams := rr.nextChunk(6, false, len(udpSegPort10000)+len(udpSegRouteTypeStatic))
	if len(ams) == 6 {
		dev.AmsNetID = fmt.Sprintf(
			"%d.%d.%d.%d.%d.%d",
			ams[0], ams[1], ams[2], ams[3], ams[4], ams[5],
		)
	}

	//
	// --------------------------------------------------------
	// Gerätename
	// --------------------------------------------------------
	//
	bNameLen := rr.nextChunk(udpLenNameLen, false, 0)

	nameLen := 0
	if len(bNameLen) == 4 && bNameLen[0] == 5 && bNameLen[1] == 0 {
		nameLen = int(bNameLen[2]) + int(bNameLen[3])*256
	}

	// Name selbst lesen (mit Beckhoff-typischem +1 Offset)
	bName := rr.nextChunk(nameLen-1, false, 1)
	dev.Name = string(bName)

	//
	// --------------------------------------------------------
	// TwinCAT-Typ (Runtime / Engineering)
	// --------------------------------------------------------
	//
	tcatType := rr.nextChunk(8, false, 0)
	if len(tcatType) == 8 && tcatType[0] == 4 {
		if tcatType[2] == udpSegTcatRuntime[2] {
			dev.IsRuntime = "X"
		} else if tcatType[2] == udpSegTcatEngineering[2] {
			dev.IsRuntime = ""
		}
	}

	//
	// --------------------------------------------------------
	// OS-Version
	// --------------------------------------------------------
	//
	osVer := rr.nextChunk(udpLenOSVersion, false, 0)
	if len(osVer) == 12 {
		osKey := uint16(osVer[0])*256 + uint16(osVer[4])
		osBuildKey := uint16(osVer[8])*256 + uint16(osVer[9])

		os := udpOsIDs[osKey]
		if os == "" {
			os = fmt.Sprintf("%X2", osKey)
		}

		if strings.Contains(os, "Windows") {
			build := udpOsBuildIDs[osBuildKey]
			if build == "" {
				build = fmt.Sprintf("%X2", osBuildKey)
			}
			dev.OsVersion = os + " " + build
		} else if osKey > 0x0C00 {
			dev.OsVersion = fmt.Sprintf("TwinCAT/BSD (%d.%d)", osVer[0], osVer[4])
		} else if osKey > 0x0601 && osKey < 0x0700 {
			dev.OsVersion = fmt.Sprintf("Linux (%d.%d)", osVer[0], osVer[4])
		} else if osKey < 0x0500 {
			dev.OsVersion = fmt.Sprintf("TC/RTOS (%d.%d)", osVer[0], osVer[4])
		} else {
			dev.OsVersion = os
		}
	}

	//
	// --------------------------------------------------------
	// Rest des Telegramms ("Tail")
	// --------------------------------------------------------
	//
	// Der Tail enthält je nach Gerät weitere Informationen wie:
	// - Hostname
	// - TwinCAT Version
	// - Fingerprint
	// - Kommentar
	tail := rr.nextChunk(len(rr.Buffer)-rr.Shift, true, 0)

	//
	// --------------------------------------------------------
	// Hostname (heuristisch)
	// --------------------------------------------------------
	//
	// Der Hostname steckt nicht immer sauber dokumentiert im Paket,
	// deshalb wird er hier heuristisch wie in der C#-Version extrahiert.
	if len(tail) > 339 && tail[337] == 20 {
		hLen := int(tail[339])

		if hLen > 1 && hLen < 253 {
			hostnameBuf := make([]byte, 253)

			for j := len(tail) - 2; j > (len(tail) - 2 - hLen); j-- {
				hostnameBuf[j-(len(tail)-2-hLen)] = tail[j]
			}

			raw := string(hostnameBuf)
			if len(raw) >= 2 && (hLen-1) <= len(raw)-2 {
				dev.Hostname = raw[2 : 2+(hLen-1)]
			}
		}
	}

	//
	// --------------------------------------------------------
	// TwinCAT-Version finden
	// --------------------------------------------------------
	//
	// Die Position der Versionsbytes ist nicht immer gleich.
	// Deshalb wird der Tail rückwärts nach bekannten Mustern durchsucht.
	//
	ci := len(tail) - 4
	tc3FingerprintLen := 69

	get := func(idx int) byte {
		if idx < 0 || idx >= len(tail) {
			return 0
		}
		return tail[idx]
	}

	for i := ci; i > 0; i -= 4 {
		// TwinCAT 2
		if get(i+0) == 3 && get(i+2) == 4 {
			dev.TcVersion.Version = get(i + 4)
			dev.TcVersion.Revision = get(i + 5)
			dev.TcVersion.Build = int16(uint16(get(i+6)) + uint16(get(i+7))*256)
			break
		}

		// TwinCAT 3
		if get((i-tc3FingerprintLen)+0) == 3 && get((i-tc3FingerprintLen)+2) == 4 {
			j := i - tc3FingerprintLen
			dev.TcVersion.Version = get(j + 4)
			dev.TcVersion.Revision = get(j + 5)
			dev.TcVersion.Build = int16(uint16(get(j+6)) + uint16(get(j+7))*256)
			break
		}

		// TwinCAT 3 mit Hostname im Tail
		if len(tail) > 339 {
			hLen := int(get(339))
			if get((i-tc3FingerprintLen-hLen)+0) == 3 &&
				get((i-tc3FingerprintLen-hLen)+2) == 4 {
				j := i - tc3FingerprintLen - hLen
				dev.TcVersion.Version = get(j + 4)
				dev.TcVersion.Revision = get(j + 5)
				dev.TcVersion.Build = int16(uint16(get(j+6)) + uint16(get(j+7))*256)
				break
			}
		}
	}

	//
	// --------------------------------------------------------
	// Runtime-Hinweis für TC3
	// --------------------------------------------------------
	//
	// In der C#-Version wird bei TwinCAT 3 bewusst "no Info" gesetzt,
	// da dieser UDP-Hinweis dort nicht zuverlässig genug ist.
	if strings.HasPrefix(dev.TcVersion.String(), "3.") {
		dev.IsRuntime = "no Info"
	}

	//
	// --------------------------------------------------------
	// Optionaler Kommentar
	// --------------------------------------------------------
	//
	descMarker := rr.nextChunk(udpLenDescMarker, false, 0)
	if len(descMarker) == 4 && descMarker[0] == 2 {
		start := rr.Shift
		if start >= 0 && start < len(rr.Buffer) {
			end := start
			for end < len(rr.Buffer) && rr.Buffer[end] != 0 {
				end++
			}
			if end > start {
				dev.Comment = string(rr.Buffer[start:end])
				rr.Shift = end
			}
		}
	}

	return dev
}

// utf16LEToASCII konvertiert UTF-16-LE nach einfachem ASCII.
//
// Aktuell wird die Funktion in dieser Datei nicht aktiv verwendet,
// bleibt aber als Hilfsfunktion erhalten, falls später Geräte
// Unicode-Felder im Tail ausliefern.
func utf16LEToASCII(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}

	var out strings.Builder

	for i := 0; i < len(b); i += 2 {
		r := rune(binary.LittleEndian.Uint16(b[i : i+2]))

		if r == 0 {
			break
		}

		if r > 127 {
			out.WriteByte('?')
		} else {
			out.WriteByte(byte(r))
		}
	}

	return out.String()
}

// ------------------------------------------------------------
// UDP Broadcast Discovery ausführen
// ------------------------------------------------------------
//
// Ablauf:
// 1. lokale Testnetz-IP bestimmen
// 2. Broadcast-Adresse berechnen
// 3. Discovery-Paket senden
// 4. Antworten bis Timeout einsammeln
// 5. Antworten parsen und sortieren
func discoverPlcsUDP(ctx context.Context, timeout time.Duration) ([]RemotePlcInfo, error) {
	localIP, mask, err := getLocalLabIPv4()
	if err != nil {
		return nil, err
	}

	bc, err := broadcastAddr(localIP, mask)
	if err != nil {
		return nil, err
	}

	// UDP-Socket auf lokaler Testnetz-IP öffnen
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: localIP, Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := buildDiscoverRequest(localIP)

	// Discovery-Request an Broadcast senden
	_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	_, err = conn.WriteToUDP(req, &net.UDPAddr{IP: bc, Port: udpPort48899})
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 4096)
	var out []RemotePlcInfo

	for {
		select {
		case <-ctx.Done():
			// Vorzeitig abbrechen: bereits gesammelte Geräte sortiert zurückgeben
			sort.Slice(out, func(i, j int) bool {
				return bytes.Compare(out[i].Address.To4(), out[j].Address.To4()) < 0
			})
			return out, ctx.Err()
		default:
		}

		_ = conn.SetReadDeadline(deadline)

		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			// Read-Timeout beendet regulär die Sammelphase
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			return nil, err
		}

		rr := &udpResponseResult{
			Buffer:     append([]byte(nil), buf[:n]...),
			RemoteHost: raddr.IP,
			Shift:      0,
		}

		dev := parseBroadcastSearchResponse(rr)
		out = append(out, dev)
	}

	// Ergebnis nach IP sortieren
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i].Address.To4(), out[j].Address.To4()) < 0
	})

	return out, nil
}
