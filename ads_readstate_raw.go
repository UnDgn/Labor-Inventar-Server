package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ------------------------------------------------------------
// ADS / AMS-TCP Konstanten
// ------------------------------------------------------------
//
// 48898 = Standard ADS/TCP Port
// 0x0004 = ADS ReadState Command
//
// state flags:
// 0x0004 = Request
// 0x0005 = Response
//
// adsClientAmsPort:
// frei gewählter lokaler AMS-Port im Header.
// Dieser ist nicht der TCP-Port, sondern der AMS-Quellport im Protokoll.
const (
	adsTCPPort          = 48898
	adsCommandReadState = uint16(0x0004)

	adsStateFlagRequest  = uint16(0x0004)
	adsStateFlagResponse = uint16(0x0005) // response bit + ADS command bit

	adsClientAmsPort = uint16(30000) // fester Client-Port im AMS-Header
)

// adsInvokeID wird für jede ADS-Anfrage erhöht,
// damit Request und Response sicher zusammenpassen.
var adsInvokeID uint32 = 1000

// ------------------------------------------------------------
// Antwortstruktur für ADS ReadState
// ------------------------------------------------------------
//
// AdsState:
//
//	TwinCAT-/ADS-Zustand (z. B. RUN / STOP / CONFIG)
//
// DeviceState:
//
//	zusätzlicher gerätespezifischer Statuswert aus Beckhoff-Sicht
type AdsReadStateResponse struct {
	AdsState    uint16
	DeviceState uint16
}

// ------------------------------------------------------------
// AMS Net ID parsen
// ------------------------------------------------------------
//
// parseAmsNetID wandelt einen String wie
//
//	"172.17.76.23.1.1"
//
// in ein [6]byte-Array um.
//
// Das ist nötig, weil der AMS-Header die Net ID binär erwartet.
func parseAmsNetID(s string) ([6]byte, error) {
	var out [6]byte

	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 6 {
		return out, fmt.Errorf("invalid AMS NetID %q", s)
	}

	for i := 0; i < 6; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 || n > 255 {
			return out, fmt.Errorf("invalid AMS NetID %q", s)
		}
		out[i] = byte(n)
	}

	return out, nil
}

// ------------------------------------------------------------
// Lokale AMS Net ID aus lokaler IPv4 ableiten
// ------------------------------------------------------------
//
// Schema:
//
//	<lokale IPv4>.1.1
//
// Beispiel:
//
//	172.17.76.23 -> 172.17.76.23.1.1
func localAmsNetIDFromIP(localIP net.IP) ([6]byte, error) {
	var out [6]byte

	ip4 := localIP.To4()
	if ip4 == nil {
		return out, fmt.Errorf("local IP is not IPv4: %v", localIP)
	}

	copy(out[0:4], ip4)
	out[4] = 1
	out[5] = 1

	return out, nil
}

// ------------------------------------------------------------
// ADS ReadState Paket bauen
// ------------------------------------------------------------
//
// buildAdsReadStatePacket erzeugt ein vollständiges AMS/TCP-Request-Paket
// für den ADS-Befehl "ReadState".
//
// Paketaufbau:
//
//	6 Byte  AMS/TCP Header
//	32 Byte AMS Header
//	0 Byte  ADS-Daten (ReadState braucht keinen Payload)
//
// Ziel:
//
//	targetNetID + targetPort
//
// Quelle:
//
//	sourceNetID + sourcePort
//
// invokeID:
//
//	eindeutige Request-ID, damit die Antwort eindeutig geprüft werden kann.
func buildAdsReadStatePacket(
	targetNetID [6]byte,
	targetPort uint16,
	sourceNetID [6]byte,
	sourcePort uint16,
	invokeID uint32,
) []byte {
	// 6 Byte AMS/TCP Header + 32 Byte AMS Header
	pkt := make([]byte, 6+32)

	//
	// --------------------------------------------------------
	// AMS/TCP Header
	// --------------------------------------------------------
	//
	// Bytes 0..1 = reserved / immer 0
	// Bytes 2..5 = Länge des AMS-Headers + ADS-Daten
	binary.LittleEndian.PutUint32(pkt[2:6], 32)

	//
	// --------------------------------------------------------
	// AMS Header
	// --------------------------------------------------------
	//
	// Ziel AMS Net ID
	copy(pkt[6:12], targetNetID[:])

	// Ziel AMS Port
	binary.LittleEndian.PutUint16(pkt[12:14], targetPort)

	// Quell AMS Net ID
	copy(pkt[14:20], sourceNetID[:])

	// Quell AMS Port
	binary.LittleEndian.PutUint16(pkt[20:22], sourcePort)

	// ADS Command = ReadState
	binary.LittleEndian.PutUint16(pkt[22:24], adsCommandReadState)

	// State Flags = Request
	binary.LittleEndian.PutUint16(pkt[24:26], adsStateFlagRequest)

	// Data Length = 0 (ReadState Request hat keinen Nutzdatenblock)
	binary.LittleEndian.PutUint32(pkt[26:30], 0)

	// AMS Error = 0 bei Request
	binary.LittleEndian.PutUint32(pkt[30:34], 0)

	// Invoke ID = eindeutige Request-ID
	binary.LittleEndian.PutUint32(pkt[34:38], invokeID)

	return pkt
}

// ------------------------------------------------------------
// ADS ReadState direkt per AMS/TCP ausführen
// ------------------------------------------------------------
//
// readAdsStateRaw ist der zentrale Low-Level-ReadState-Mechanismus.
//
// Ablauf:
// 1. Remote AMS Net ID parsen
// 2. lokale AMS Net ID aus lokaler IP erzeugen
// 3. Request-Paket bauen
// 4. TCP-Verbindung zu ADS-Port 48898 aufbauen
// 5. Request senden
// 6. AMS/TCP Header lesen
// 7. AMS Payload lesen
// 8. Antwort validieren
// 9. AdsState + DeviceState extrahieren
//
// Diese Funktion ersetzt bewusst die frühere externe ADS-Library,
// damit das Protokoll vollständig unter eigener Kontrolle steht.
func readAdsStateRaw(
	remoteIP net.IP,
	remoteAmsNetID string,
	amsPort uint16,
	localIP net.IP,
	timeout time.Duration,
) (AdsReadStateResponse, error) {
	var out AdsReadStateResponse

	//
	// --------------------------------------------------------
	// Ziel- und Quell-NetID vorbereiten
	// --------------------------------------------------------
	//
	targetNetID, err := parseAmsNetID(remoteAmsNetID)
	if err != nil {
		return out, err
	}

	sourceNetID, err := localAmsNetIDFromIP(localIP)
	if err != nil {
		return out, err
	}

	//
	// --------------------------------------------------------
	// Eindeutige InvokeID vergeben und Request bauen
	// --------------------------------------------------------
	//
	invokeID := atomic.AddUint32(&adsInvokeID, 1)

	req := buildAdsReadStatePacket(
		targetNetID,
		amsPort,
		sourceNetID,
		adsClientAmsPort,
		invokeID,
	)

	//
	// --------------------------------------------------------
	// TCP-Verbindung zu ADS/TCP öffnen
	// --------------------------------------------------------
	//
	conn, err := net.DialTimeout(
		"tcp4",
		net.JoinHostPort(remoteIP.String(), strconv.Itoa(adsTCPPort)),
		timeout,
	)
	if err != nil {
		return out, fmt.Errorf("dial tcp %s:%d: %w", remoteIP.String(), adsTCPPort, err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))

	//
	// --------------------------------------------------------
	// Request senden
	// --------------------------------------------------------
	//
	if _, err := conn.Write(req); err != nil {
		return out, fmt.Errorf("write request: %w", err)
	}

	//
	// --------------------------------------------------------
	// AMS/TCP Header lesen
	// --------------------------------------------------------
	//
	// Der Header ist 6 Byte lang.
	// Darin steht u. a. die Länge des nachfolgenden AMS-Payloads.
	tcpHdr := make([]byte, 6)
	if _, err := io.ReadFull(conn, tcpHdr); err != nil {
		return out, fmt.Errorf("read ams/tcp header: %w", err)
	}

	payloadLen := binary.LittleEndian.Uint32(tcpHdr[2:6])
	if payloadLen < 32 {
		return out, fmt.Errorf("invalid AMS/TCP payload length: %d", payloadLen)
	}

	//
	// --------------------------------------------------------
	// AMS Payload lesen
	// --------------------------------------------------------
	//
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return out, fmt.Errorf("read AMS payload: %w", err)
	}

	if len(payload) < 32 {
		return out, fmt.Errorf("AMS payload too short: %d", len(payload))
	}

	//
	// --------------------------------------------------------
	// AMS Header der Antwort auswerten
	// --------------------------------------------------------
	//
	cmdID := binary.LittleEndian.Uint16(payload[16:18])
	stateFlags := binary.LittleEndian.Uint16(payload[18:20])
	dataLen := binary.LittleEndian.Uint32(payload[20:24])
	amsErr := binary.LittleEndian.Uint32(payload[24:28])
	respInvokeID := binary.LittleEndian.Uint32(payload[28:32])

	// Antwort muss zur Anfrage passen
	if respInvokeID != invokeID {
		return out, fmt.Errorf("invoke id mismatch: got %d want %d", respInvokeID, invokeID)
	}

	// Antwort muss wirklich auf ReadState kommen
	if cmdID != adsCommandReadState {
		return out, fmt.Errorf("unexpected ADS command: 0x%04X", cmdID)
	}

	// Antwort muss als Response markiert sein
	if stateFlags != adsStateFlagResponse {
		return out, fmt.Errorf("unexpected state flags: 0x%04X", stateFlags)
	}

	// AMS-Fehler auf Header-Ebene
	if amsErr != 0 {
		return out, fmt.Errorf("AMS error: 0x%08X", amsErr)
	}

	// ReadState Response-Datenblock ist immer 8 Byte:
	// 4 Byte ADS Result + 2 Byte AdsState + 2 Byte DeviceState
	if dataLen != 8 {
		return out, fmt.Errorf("unexpected ReadState data length: got %d want 8", dataLen)
	}

	if len(payload) < 32+8 {
		return out, fmt.Errorf("payload too short for ReadState data: %d", len(payload))
	}

	//
	// --------------------------------------------------------
	// ADS-Datenblock lesen
	// --------------------------------------------------------
	//
	data := payload[32 : 32+8]

	// ADS-Result innerhalb des Datenblocks
	adsResult := binary.LittleEndian.Uint32(data[0:4])
	if adsResult != 0 {
		return out, fmt.Errorf("ADS error: 0x%08X", adsResult)
	}

	// Eigentliche Nutzdaten
	out.AdsState = binary.LittleEndian.Uint16(data[4:6])
	out.DeviceState = binary.LittleEndian.Uint16(data[6:8])

	return out, nil
}
