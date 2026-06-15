package hdhr

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
)

const (
	discoveryPort = 65001
	maxPacketSize = 1460

	packetDiscoverRequest = 0x0002
	packetDiscoverReply   = 0x0003

	tagDeviceType = 0x01
	tagDeviceID   = 0x02
	tagTunerCount = 0x10
	tagLineupURL  = 0x27
	tagBaseURL    = 0x2A

	deviceTypeTuner    = 0x00000001
	wildcardDeviceType = 0xFFFFFFFF
	wildcardDeviceID   = 0xFFFFFFFF
)

type ConfigProvider func() config.Config
type ReadyProvider func() bool
type TunerProvider func() int

type Service struct {
	cfg    ConfigProvider
	ready  ReadyProvider
	tuners TunerProvider
	log    *logging.Logger
}

func New(cfg ConfigProvider, ready ReadyProvider, tuners TunerProvider, logger *logging.Logger) *Service {
	return &Service{cfg: cfg, ready: ready, tuners: tuners, log: logger}
}

func (s *Service) Run(ctx context.Context) {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: discoveryPort}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		s.log.Warn("HDHomeRun discovery disabled: could not listen on UDP %d: %v", discoveryPort, err)
		return
	}
	defer conn.Close()
	s.log.Always("HDHomeRun discovery is listening on udp://0.0.0.0:%d.", discoveryPort)

	buf := make([]byte, maxPacketSize)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			s.log.Warn("HDHomeRun discovery read deadline failed: %v", err)
			return
		}
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			s.log.Warn("HDHomeRun discovery read failed: %v", err)
			continue
		}
		if !s.ready() {
			continue
		}
		response, ok := DiscoveryResponse(s.cfg(), s.tuners(), buf[:n])
		if !ok {
			continue
		}
		if _, err := conn.WriteToUDP(response, remote); err != nil {
			s.log.Warn("HDHomeRun discovery response failed: %v", err)
			continue
		}
		s.log.Always("HDHomeRun discovery request answered for %s.", remote.String())
	}
}

func DiscoveryResponse(cfg config.Config, tuners int, request []byte) ([]byte, bool) {
	discovery, ok := ParseDiscoveryRequest(request)
	if !ok {
		return nil, false
	}
	deviceID := parseDeviceID(cfg.DeviceID)
	if discovery.deviceType != wildcardDeviceType && discovery.deviceType != deviceTypeTuner {
		return nil, false
	}
	if discovery.deviceID != wildcardDeviceID && discovery.deviceID != deviceID {
		return nil, false
	}
	if tuners <= 0 {
		tuners = 2
	}
	payload := make([]byte, 0, 128)
	payload = appendU32TLV(payload, tagDeviceType, deviceTypeTuner)
	payload = appendU32TLV(payload, tagDeviceID, deviceID)
	payload = appendU8TLV(payload, tagTunerCount, byte(tuners))
	payload = appendStringTLV(payload, tagBaseURL, strings.TrimRight(cfg.ServerURL, "/"))
	payload = appendStringTLV(payload, tagLineupURL, strings.TrimRight(cfg.ServerURL, "/")+"/lineup.json")
	return sealFrame(packetDiscoverReply, payload), true
}

type discoveryRequest struct {
	deviceType uint32
	deviceID   uint32
}

func ParseDiscoveryRequest(packet []byte) (discoveryRequest, bool) {
	frameType, payload, ok := openFrame(packet)
	if !ok || frameType != packetDiscoverRequest {
		return discoveryRequest{}, false
	}
	req := discoveryRequest{deviceType: wildcardDeviceType, deviceID: wildcardDeviceID}
	for len(payload) > 0 {
		tag := payload[0]
		payload = payload[1:]
		length, rest, ok := readVarLength(payload)
		if !ok || len(rest) < length {
			return discoveryRequest{}, false
		}
		value := rest[:length]
		payload = rest[length:]
		switch tag {
		case tagDeviceType:
			if len(value) == 4 {
				req.deviceType = binary.BigEndian.Uint32(value)
			}
		case tagDeviceID:
			if len(value) == 4 {
				req.deviceID = binary.BigEndian.Uint32(value)
			}
		}
	}
	return req, true
}

func openFrame(packet []byte) (uint16, []byte, bool) {
	if len(packet) < 8 {
		return 0, nil, false
	}
	expectedCRC := binary.LittleEndian.Uint32(packet[len(packet)-4:])
	if crc32.ChecksumIEEE(packet[:len(packet)-4]) != expectedCRC {
		return 0, nil, false
	}
	frameType := binary.BigEndian.Uint16(packet[0:2])
	payloadLength := int(binary.BigEndian.Uint16(packet[2:4]))
	if payloadLength != len(packet)-8 {
		return 0, nil, false
	}
	return frameType, packet[4 : 4+payloadLength], true
}

func sealFrame(frameType uint16, payload []byte) []byte {
	packet := make([]byte, 4, len(payload)+8)
	binary.BigEndian.PutUint16(packet[0:2], frameType)
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(payload)))
	packet = append(packet, payload...)
	crc := crc32.ChecksumIEEE(packet)
	packet = binary.LittleEndian.AppendUint32(packet, crc)
	return packet
}

func appendU32TLV(payload []byte, tag byte, value uint32) []byte {
	payload = append(payload, tag)
	payload = appendVarLength(payload, 4)
	return binary.BigEndian.AppendUint32(payload, value)
}

func appendU8TLV(payload []byte, tag byte, value byte) []byte {
	payload = append(payload, tag)
	payload = appendVarLength(payload, 1)
	return append(payload, value)
}

func appendStringTLV(payload []byte, tag byte, value string) []byte {
	payload = append(payload, tag)
	payload = appendVarLength(payload, len(value))
	return append(payload, value...)
}

func appendVarLength(payload []byte, length int) []byte {
	if length <= 127 {
		return append(payload, byte(length))
	}
	return append(payload, byte(length&0x7F)|0x80, byte(length>>7))
}

func readVarLength(payload []byte) (int, []byte, bool) {
	if len(payload) == 0 {
		return 0, nil, false
	}
	length := int(payload[0] & 0x7F)
	if payload[0]&0x80 == 0 {
		return length, payload[1:], true
	}
	if len(payload) < 2 {
		return 0, nil, false
	}
	length |= int(payload[1]) << 7
	return length, payload[2:], true
}

func parseDeviceID(value string) uint32 {
	clean := strings.TrimPrefix(strings.TrimSpace(value), "0x")
	clean = strings.TrimPrefix(clean, "0X")
	if clean == "" {
		return 0x12345679
	}
	parsed, err := strconv.ParseUint(clean, 16, 32)
	if err == nil {
		return uint32(parsed)
	}
	parsed, err = strconv.ParseUint(clean, 10, 32)
	if err == nil {
		return uint32(parsed)
	}
	return 0x12345679
}

func BuildDiscoveryRequest(deviceType, deviceID uint32) []byte {
	payload := make([]byte, 0, 16)
	payload = appendU32TLV(payload, tagDeviceType, deviceType)
	payload = appendU32TLV(payload, tagDeviceID, deviceID)
	return sealFrame(packetDiscoverRequest, payload)
}

func DebugPacket(packet []byte) string {
	return fmt.Sprintf("% X", packet)
}
