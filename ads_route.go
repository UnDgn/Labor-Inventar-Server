package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"time"

	ads "github.com/stamp/goADS"
)

type PlcStateResult struct {
	Status string // RUN / STOP / CONFIG / ...
	Err    string // optional debug
}

const (
	routeUdpPort48899  = 48899
	routePlcRuntime851 = uint16(851) // TC3 PLC Runtime 1
)

// --- Segmente (Prefix route...) ---
var (
	routeSegHeader          = []byte{0x03, 0x66, 0x14, 0x71}
	routeSegEnd             = []byte{0, 0, 0, 0}
	routeSegPort10000       = []byte{0x10, 0x27}    // 10000 little-endian
	routeSegReqAddRoute     = []byte{6, 0, 0, 0}    // REQUEST_ADDROUTE
	routeSegRespAddRoute    = []byte{6, 0, 0, 0x80} // RESPONSE_ADDROUTE
	routeSegRouteTypeStatic = []byte{5, 0, 0, 0}    // ROUTETYPE_STATIC

	routeSegRouteNameL = []byte{0x0c, 0, 0, 0} // ROUTENAME_L
	routeSegUserNameL  = []byte{0x0d, 0, 0, 0} // USERNAME_L
	routeSegPasswordL  = []byte{2, 0, 0, 0}    // PASSWORD_L
	routeSegLocalHostL = []byte{5, 0, 0, 0}    // LOCALHOST_L
	routeSegAmsNetIdL  = []byte{7, 0, 6, 0}    // AMSNETID_L
)

// ASCII + \0 (wie Extensions.GetAdsBytes)
func asciiZ(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, []byte(s))
	b[len(b)-1] = 0
	return b
}

func buildAddRoutePacket(localAmsNetID [6]byte, routeName, user, pass, localHostNameOrIP string) []byte {
	routeNameB := asciiZ(routeName)
	userB := asciiZ(user)
	passB := asciiZ(pass)
	localHostB := asciiZ(localHostNameOrIP)

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
	out = append(out, routeSegUserNameL...)
	out = append(out, userB...)
	out = append(out, routeSegPasswordL...)
	out = append(out, passB...)
	out = append(out, routeSegLocalHostL...)
	out = append(out, localHostB...)
	return out
}

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
	shift := 4 + 4 + 4 + 6 + 2 + 4 + 4
	ack := pkt[shift : shift+4]
	return ack[0] == 0 && ack[1] == 0
}

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

	var localAms [6]byte
	copy(localAms[0:4], ip4)
	localAms[4] = 1
	localAms[5] = 1

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

	hostName, _ := os.Hostname()
	localHostNameOrIP := hostName
	if localHostNameOrIP == "" {
		localHostNameOrIP = ip4.String()
	}

	req := buildAddRoutePacket(localAms, routeName, user, pass, localHostNameOrIP)

	// ✅ Wichtig: unconnected UDP wie in C#
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

	// senden
	_, err = conn.WriteToUDP(req, &net.UDPAddr{IP: rip4, Port: udpPort})
	if err != nil {
		return err
	}

	// empfangen (Antwort kann von anderem Source-Port kommen → ReadFromUDP!)
	buf := make([]byte, 2048)
	for {
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err // timeout oder echtes socket problem
		}

		// optional: nur Antworten vom Zielhost akzeptieren
		if !raddr.IP.Equal(rip4) {
			continue
		}

		if parseAddRouteAck(buf[:n]) {
			return nil
		}

		// falls andere Pakete kommen: weiter warten bis deadline
	}
}

// TryReadPlcState: legt Route an (UDP, mit Credentials) und macht dann ADS-TCP ReadState (Port 851).
func TryReadPlcState(ctx context.Context, localIP net.IP, remoteIP net.IP, remoteAmsNetID string) PlcStateResult {
	// 1) Route anlegen
	{
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		hostName, _ := os.Hostname()

		if err := EnsureAdsRouteUDP(
			cctx,
			localIP,
			remoteIP,
			hostName, // <-- statt "GoInventory"
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

	// 2) ADS-TCP ReadState
	conn, err := ads.NewConnection(remoteIP.String(), remoteAmsNetID, routePlcRuntime851)
	if err != nil {
		return PlcStateResult{Status: "no Info", Err: "NewConnection: " + err.Error()}
	}
	defer conn.Close()

	conn.Connect()

	st, err := conn.ReadState()
	if err != nil {
		return PlcStateResult{Status: "no Info", Err: "ReadState: " + err.Error()}
	}

	switch st.ADSState {
	case 5:
		return PlcStateResult{Status: "RUN", Err: ""}
	case 6:
		return PlcStateResult{Status: "STOP", Err: ""}
	case 15:
		return PlcStateResult{Status: "CONFIG", Err: ""}
	default:
		return PlcStateResult{Status: fmt.Sprintf("State %d", st.ADSState), Err: ""}
	}
}
