package hdhr

import (
	"strings"
	"testing"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
)

func TestDiscoveryResponseForWildcardRequest(t *testing.T) {
	cfg := config.Config{
		DeviceID:  "12345679",
		ServerURL: "http://192.168.1.20:8181",
	}
	request := BuildDiscoveryRequest(deviceTypeTuner, wildcardDeviceID)

	response, ok := DiscoveryResponse(cfg, 4, request)
	if !ok {
		t.Fatal("expected discovery response")
	}
	frameType, payload, ok := openFrame(response)
	if !ok {
		t.Fatalf("response frame did not validate: %s", DebugPacket(response))
	}
	if frameType != packetDiscoverReply {
		t.Fatalf("expected discover reply frame, got %#04x", frameType)
	}
	values := parseTLVs(t, payload)
	if got := values[tagDeviceType]; len(got) != 4 || got[3] != 0x01 {
		t.Fatalf("missing tuner device type: % X", got)
	}
	if got := values[tagDeviceID]; len(got) != 4 || got[0] != 0x12 || got[1] != 0x34 || got[2] != 0x56 || got[3] != 0x79 {
		t.Fatalf("missing configured device id: % X", got)
	}
	if got := values[tagTunerCount]; len(got) != 1 || got[0] != 4 {
		t.Fatalf("missing tuner count: % X", got)
	}
	if got := string(values[tagBaseURL]); got != "http://192.168.1.20:8181" {
		t.Fatalf("unexpected base url: %q", got)
	}
	if got := string(values[tagLineupURL]); got != "http://192.168.1.20:8181/lineup.json" {
		t.Fatalf("unexpected lineup url: %q", got)
	}
}

func TestDiscoveryResponseIgnoresNonMatchingDeviceID(t *testing.T) {
	cfg := config.Config{DeviceID: "12345679", ServerURL: "http://192.168.1.20:8181"}
	request := BuildDiscoveryRequest(deviceTypeTuner, 0x87654321)

	if response, ok := DiscoveryResponse(cfg, 2, request); ok || response != nil {
		t.Fatalf("expected non-matching device request to be ignored, got ok=%v response=% X", ok, response)
	}
}

func TestDiscoveryResponseIgnoresBadCRC(t *testing.T) {
	cfg := config.Config{DeviceID: "12345679", ServerURL: "http://192.168.1.20:8181"}
	request := BuildDiscoveryRequest(deviceTypeTuner, wildcardDeviceID)
	request[len(request)-1] ^= 0xFF

	if response, ok := DiscoveryResponse(cfg, 2, request); ok || response != nil {
		t.Fatalf("expected invalid packet to be ignored, got ok=%v response=% X", ok, response)
	}
}

func parseTLVs(t *testing.T, payload []byte) map[byte][]byte {
	t.Helper()
	values := map[byte][]byte{}
	for len(payload) > 0 {
		tag := payload[0]
		length, rest, ok := readVarLength(payload[1:])
		if !ok {
			t.Fatalf("invalid tlv length in %s", strings.ToUpper(DebugPacket(payload)))
		}
		if len(rest) < length {
			t.Fatalf("short tlv value in %s", strings.ToUpper(DebugPacket(payload)))
		}
		values[tag] = rest[:length]
		payload = rest[length:]
	}
	return values
}
