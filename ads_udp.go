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

// UDP-Discovery Ergebnis (Portierung von RemotePlcInfo)
type RemotePlcInfo struct {
	Name        string
	Address     net.IP
	AmsNetID    string
	OsVersion   string
	Fingerprint string
	Comment     string
	TcVersion   AdsVersion
	IsRuntime   string
	Hostname    string
}

type AdsVersion struct {
	Version  uint8
	Revision uint8
	Build    int16
}

func (v AdsVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Version, v.Revision, v.Build)
}

// --- UDP Segments (Prefix udp...) ---
var (
	udpSegHeader           = []byte{0x03, 0x66, 0x14, 0x71}
	udpSegEnd              = []byte{0, 0, 0, 0}
	udpSegRequestDiscover  = []byte{1, 0, 0, 0}
	udpSegResponseDiscover = []byte{1, 0, 0, 0x80}
	udpSegPort10000        = []byte{0x10, 0x27} // 10000 little endian
	udpSegRouteTypeStatic  = []byte{5, 0, 0, 0}

	udpSegTcatRuntime     = []byte{4, 0, 0x14, 1, 0x14, 1, 0, 0}
	udpSegTcatEngineering = []byte{4, 0, 0x94, 0, 0x94, 0, 0, 0}

	udpLenNameLen    = 4
	udpLenOSVersion  = 12
	udpLenDescMarker = 4

	udpPort48899 = 48899
)

// OS Dictionaries wie in C#
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

// Hilfsstruktur wie ResponseResult + NextChunk
type udpResponseResult struct {
	Buffer     []byte
	RemoteHost net.IP
	Shift      int
}

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

func isInLabSubnetIP(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 172 && ip4[1] == 17 && ip4[2] == 76
}

// Wird von scanner.go genutzt
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
	// AMS = ip0.ip1.ip2.ip3.1.1
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

// 1:1 Portierung (gekürzt, aber funktional) von C# ParseBroadcastSearchResponse
func parseBroadcastSearchResponse(rr *udpResponseResult) RemotePlcInfo {
	dev := RemotePlcInfo{Address: rr.RemoteHost}

	if len(rr.Buffer) < 12 {
		return dev
	}
	if !bytes.Equal(rr.Buffer[0:4], udpSegHeader) {
		return dev
	}
	if !bytes.Equal(rr.Buffer[4:8], udpSegEnd) {
		return dev
	}
	if !bytes.Equal(rr.Buffer[8:12], udpSegResponseDiscover) {
		return dev
	}

	rr.Shift = 4 + 4 + 4

	// AmsNetId + skip port(2) + routeType(4)
	ams := rr.nextChunk(6, false, len(udpSegPort10000)+len(udpSegRouteTypeStatic))
	if len(ams) == 6 {
		dev.AmsNetID = fmt.Sprintf("%d.%d.%d.%d.%d.%d", ams[0], ams[1], ams[2], ams[3], ams[4], ams[5])
	}

	// NameLength
	bNameLen := rr.nextChunk(udpLenNameLen, false, 0)
	nameLen := 0
	if len(bNameLen) == 4 && bNameLen[0] == 5 && bNameLen[1] == 0 {
		nameLen = int(bNameLen[2]) + int(bNameLen[3])*256
	}
	// Name (nameLen-1) + add 1
	bName := rr.nextChunk(nameLen-1, false, 1)
	dev.Name = string(bName)

	// TwinCAT Type (8)
	tcatType := rr.nextChunk(8, false, 0)
	if len(tcatType) == 8 && tcatType[0] == 4 {
		if tcatType[2] == udpSegTcatRuntime[2] {
			dev.IsRuntime = "X"
		} else if tcatType[2] == udpSegTcatEngineering[2] {
			dev.IsRuntime = ""
		}
	}

	// OS Version (12)
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

	// Tail (peek)
	tail := rr.nextChunk(len(rr.Buffer)-rr.Shift, true, 0)

	// Hostname: wie C# (heuristisch)
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

	// TwinCAT version scan (C# Logik)
	ci := len(tail) - 4
	tc3FingerprintLen := 69

	get := func(idx int) byte {
		if idx < 0 || idx >= len(tail) {
			return 0
		}
		return tail[idx]
	}

	for i := ci; i > 0; i -= 4 {
		// TC2
		if get(i+0) == 3 && get(i+2) == 4 {
			dev.TcVersion.Version = get(i + 4)
			dev.TcVersion.Revision = get(i + 5)
			dev.TcVersion.Build = int16(uint16(get(i+6)) + uint16(get(i+7))*256)
			break
		}

		// TC3
		if get((i-tc3FingerprintLen)+0) == 3 && get((i-tc3FingerprintLen)+2) == 4 {
			j := i - tc3FingerprintLen
			dev.TcVersion.Version = get(j + 4)
			dev.TcVersion.Revision = get(j + 5)
			dev.TcVersion.Build = int16(uint16(get(j+6)) + uint16(get(j+7))*256)
			break
		}

		// TC3 with hostname
		if len(tail) > 339 {
			hLen := int(get(339))
			if get((i-tc3FingerprintLen-hLen)+0) == 3 && get((i-tc3FingerprintLen-hLen)+2) == 4 {
				j := i - tc3FingerprintLen - hLen
				dev.TcVersion.Version = get(j + 4)
				dev.TcVersion.Revision = get(j + 5)
				dev.TcVersion.Build = int16(uint16(get(j+6)) + uint16(get(j+7))*256)
				break
			}
		}
	}

	// C# setzt TC3 runtime bewusst auf no Info
	if strings.HasPrefix(dev.TcVersion.String(), "3.") {
		dev.IsRuntime = "no Info"
	}

	// Kommentar (optional)
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

// Discover Beckhoff devices via UDP broadcast
func discoverPlcsUDP(ctx context.Context, timeout time.Duration) ([]RemotePlcInfo, error) {
	localIP, mask, err := getLocalLabIPv4()
	if err != nil {
		return nil, err
	}
	bc, err := broadcastAddr(localIP, mask)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: localIP, Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := buildDiscoverRequest(localIP)

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
			// returns what we have
			sort.Slice(out, func(i, j int) bool {
				return bytes.Compare(out[i].Address.To4(), out[j].Address.To4()) < 0
			})
			return out, ctx.Err()
		default:
		}

		_ = conn.SetReadDeadline(deadline)
		n, raddr, err := conn.ReadFromUDP(buf)
		if err != nil {
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

	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i].Address.To4(), out[j].Address.To4()) < 0
	})
	return out, nil
}
