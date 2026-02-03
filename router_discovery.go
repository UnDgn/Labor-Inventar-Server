package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// ===== Segments aus Segment.cs =====
var (
	segHeader           = []byte{0x03, 0x66, 0x14, 0x71}
	segEnd              = []byte{0, 0, 0, 0}
	segRequestDiscover  = []byte{1, 0, 0, 0}
	segPort             = []byte{0x10, 0x27} // 0x2710 = 10000 (little endian)
	segResponseDiscover = []byte{1, 0, 0, 0x80}

	// Segment.AMSNETID = {0,0,0,0,1,1} wird im Request überschrieben: erste 4 Bytes = Localhost IP
	segAmsNetIdTemplate = []byte{0, 0, 0, 0, 1, 1}
)

// discoverTargetsUDP macht das, was AdsRouter.BroadcastSearchAsync in C# macht,
// und liefert map[ip]amsNetId (z.B. "172.17.76.162" -> "5.124.195.176.1.1").
func discoverTargetsUDP(timeout time.Duration) map[string]string {
	found := make(map[string]string)

	localIPs, err := getLocalIPv4s()
	if err != nil {
		fmt.Println("discoverTargetsUDP: getLocalIPv4s error:", err)
		return found
	}

	for _, localIP := range localIPs {
		bcast, err := broadcastAddrForLocal(localIP)
		if err != nil || bcast == nil {
			continue
		}

		// UDP Conn: wir "listen" auf ephemeral port, erlauben broadcast
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: localIP, Port: 0})
		if err != nil {
			continue
		}

		_ = conn.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
		_ = conn.SetReadDeadline(time.Now().Add(timeout))

		// Broadcast aktivieren (Go macht das über UDPConn + addr i.d.R. okay;
		// auf manchen Systemen muss man extra Control setzen; Windows klappt meist ohne.)
		req := buildDiscoverRequest(localIP)

		_, _ = conn.WriteToUDP(req, &net.UDPAddr{IP: bcast, Port: 48899})

		// Antworten bis timeout einsammeln
		buf := make([]byte, 4096)
		for {
			n, raddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				break // timeout
			}
			ams, ok := parseDiscoverResponse(buf[:n])
			if !ok {
				continue
			}
			// raddr.IP ist die IP des antwortenden Geräts (wie rr.RemoteHost)
			found[raddr.IP.String()] = ams
		}

		_ = conn.Close()
	}

	return found
}

func buildDiscoverRequest(localIP net.IP) []byte {
	ip4 := localIP.To4()
	if ip4 == nil {
		ip4 = net.IPv4(0, 0, 0, 0)
	}

	ams := make([]byte, 6)
	copy(ams, segAmsNetIdTemplate)
	copy(ams[0:4], ip4) // C#: localhost.GetAddressBytes().CopyTo(Segment_AMSNETID, 0);

	// Request = HEADER + END + REQUEST_DISCOVER + AMSNETID + PORT + END
	var out []byte
	out = append(out, segHeader...)
	out = append(out, segEnd...)
	out = append(out, segRequestDiscover...)
	out = append(out, ams...)
	out = append(out, segPort...)
	out = append(out, segEnd...)
	return out
}

func parseDiscoverResponse(pkt []byte) (string, bool) {
	if len(pkt) < 12+6 {
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

	// C# NextChunk(6, add: 2+4) => zuerst 6 lesen, danach 2+4 überspringen (für spätere Felder)
	ams := pkt[shift : shift+6]

	amsStr := fmt.Sprintf("%d.%d.%d.%d.%d.%d",
		ams[0], ams[1], ams[2], ams[3], ams[4], ams[5])

	return amsStr, true
}

func isInLabSubnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 172 && ip4[1] == 17 && ip4[2] == 76
}

// ===== IPHelper.cs Port =====

// getLocalIPv4s entspricht grob IPHelper.FilteredLocalhosts (WLAN/Ethernet) und liefert IPv4s.
func getLocalIPv4s() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var ips []net.IP
	for _, iface := range ifaces {
		// nur up + kein loopback
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			if isInLabSubnetIP(ip4) {
				ips = append(ips, ip4)
			}
		}
	}
	return ips, nil
}

func broadcastAddrForLocal(localIP net.IP) (net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	localIP4 := localIP.To4()
	if localIP4 == nil {
		return nil, fmt.Errorf("not ipv4")
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
			if !ip4.Equal(localIP4) {
				continue
			}

			mask := ipnet.Mask
			if len(mask) != 4 {
				continue
			}

			// broadcast = ip | ^mask
			b := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				b[i] = ip4[i] | ^mask[i]
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("no iface/mask for local ip %s", localIP.String())
}

// (kleiner Helfer, falls du später Ports/Endian brauchst)
func u16le(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
