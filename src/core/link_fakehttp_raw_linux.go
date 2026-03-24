//go:build linux

package core

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

type rawFakeHTTPInjector struct{}

func newFakeHTTPRawInjector() fakeHTTPInjector {
	return &rawFakeHTTPInjector{}
}

func (i *rawFakeHTTPInjector) Inject(_ context.Context, req fakeHTTPRequest) error {
	dst := req.targetIP.To4()
	if dst == nil {
		return fmt.Errorf("raw fakehttp only supports IPv4 destinations")
	}
	src, err := rawSourceIPv4(dst, req.targetPort, req.sourceInterface)
	if err != nil {
		return err
	}
	srcPort := rawRandomPort()
	seq := rawRandomUint32()
	ack := rawRandomUint32()
	payload := []byte(buildFakeHTTPRequest(req.fakeHost))
	packet := rawBuildIPv4TCPPacket(src, dst, srcPort, uint16(req.targetPort), seq, ack, req.ttl, payload)

	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_RAW)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	if req.sourceInterface != "" {
		if err = unix.BindToDevice(fd, req.sourceInterface); err != nil {
			return err
		}
	}
	if err = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_HDRINCL, 1); err != nil {
		return err
	}

	var dstAddr [4]byte
	copy(dstAddr[:], dst)
	return unix.Sendto(fd, packet, 0, &unix.SockaddrInet4{Addr: dstAddr})
}

func rawSourceIPv4(dst net.IP, port int, sintf string) (net.IP, error) {
	if sintf != "" {
		ief, err := net.InterfaceByName(sintf)
		if err != nil {
			return nil, err
		}
		if ief.Flags&net.FlagUp == 0 {
			return nil, fmt.Errorf("interface %q is not up", sintf)
		}
		addrs, err := ief.Addrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			src, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			src4 := src.To4()
			if src4 == nil {
				continue
			}
			if src.IsGlobalUnicast() != dst.IsGlobalUnicast() && src.IsLinkLocalUnicast() != dst.IsLinkLocalUnicast() {
				continue
			}
			return src4, nil
		}
		return nil, fmt.Errorf("no suitable source address found on interface %q", sintf)
	}

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: dst, Port: port})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		return nil, fmt.Errorf("unable to determine source IPv4")
	}
	src := local.IP.To4()
	if src == nil {
		return nil, fmt.Errorf("unable to determine source IPv4")
	}
	return src, nil
}

func rawBuildIPv4TCPPacket(srcIP, dstIP net.IP, srcPort, dstPort uint16, seq, ack uint32, ttl int, payload []byte) []byte {
	if ttl <= 0 {
		ttl = defaultFakeHTTPTTL
	}

	ipHeaderLen := 20
	tcpHeaderLen := 20
	totalLen := ipHeaderLen + tcpHeaderLen + len(payload)
	packet := make([]byte, totalLen)

	packet[0] = 0x45
	packet[1] = 0x00
	binary.BigEndian.PutUint16(packet[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(packet[4:6], rawRandomUint16())
	binary.BigEndian.PutUint16(packet[6:8], 0x4000)
	packet[8] = byte(ttl)
	packet[9] = unix.IPPROTO_TCP
	copy(packet[12:16], srcIP.To4())
	copy(packet[16:20], dstIP.To4())
	binary.BigEndian.PutUint16(packet[10:12], rawChecksum(packet[:ipHeaderLen]))

	tcp := packet[ipHeaderLen:]
	binary.BigEndian.PutUint16(tcp[0:2], srcPort)
	binary.BigEndian.PutUint16(tcp[2:4], dstPort)
	binary.BigEndian.PutUint32(tcp[4:8], seq)
	binary.BigEndian.PutUint32(tcp[8:12], ack)
	tcp[12] = byte((tcpHeaderLen / 4) << 4)
	tcp[13] = 0x18
	binary.BigEndian.PutUint16(tcp[14:16], 65535)
	copy(tcp[tcpHeaderLen:], payload)
	binary.BigEndian.PutUint16(tcp[16:18], rawTCPChecksum(srcIP.To4(), dstIP.To4(), tcp))

	return packet
}

func rawTCPChecksum(srcIP, dstIP net.IP, tcp []byte) uint16 {
	pseudo := make([]byte, 12+len(tcp))
	copy(pseudo[0:4], srcIP)
	copy(pseudo[4:8], dstIP)
	pseudo[8] = 0
	pseudo[9] = unix.IPPROTO_TCP
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(tcp)))
	copy(pseudo[12:], tcp)
	return rawChecksum(pseudo)
}

func rawChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for (sum >> 16) != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

func rawRandomPort() uint16 {
	return 1024 + (rawRandomUint16() % (65535 - 1024))
}

func rawRandomUint16() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0x1234
	}
	return binary.BigEndian.Uint16(b[:])
}

func rawRandomUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0x12345678
	}
	return binary.BigEndian.Uint32(b[:])
}
