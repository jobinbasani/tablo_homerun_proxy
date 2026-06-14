package ssdp

import (
	"strings"
	"testing"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
)

func TestSearchResponseForSupportedTarget(t *testing.T) {
	cfg := config.Config{
		DeviceID:  "ABC12345",
		ServerURL: "http://192.168.1.10:8181",
	}
	request := strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: 239.255.255.250:1900",
		"MAN: \"ssdp:discover\"",
		"ST: ssdp:all",
		"MX: 1",
		"",
		"",
	}, "\r\n")

	response, ok := SearchResponse(cfg, request)
	if !ok {
		t.Fatal("expected SSDP response")
	}
	if !strings.Contains(response, "LOCATION: http://192.168.1.10:8181/discover.json") {
		t.Fatalf("response missing discover location: %s", response)
	}
	if !strings.Contains(response, "USN: uuid:tablo-homerun-proxy-ABC12345") {
		t.Fatalf("response missing stable USN: %s", response)
	}
}

func TestSearchResponseIgnoresUnsupportedTarget(t *testing.T) {
	cfg := config.Config{DeviceID: "ABC12345", ServerURL: "http://192.168.1.10:8181"}
	request := strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"ST: urn:schemas-upnp-org:device:Printer:1",
		"",
		"",
	}, "\r\n")

	if response, ok := SearchResponse(cfg, request); ok || response != "" {
		t.Fatalf("expected unsupported search target to be ignored, got ok=%v response=%q", ok, response)
	}
}

func TestAliveNotificationsUseDiscoverLocationAndDeviceID(t *testing.T) {
	cfg := config.Config{DeviceID: "12345679", ServerURL: "http://10.0.0.8:8181"}
	payloads := AliveNotifications(cfg)
	if len(payloads) == 0 {
		t.Fatal("expected alive notifications")
	}
	for _, payload := range payloads {
		if !strings.Contains(payload, "NTS: ssdp:alive") {
			t.Fatalf("alive notification missing NTS: %s", payload)
		}
		if !strings.Contains(payload, "LOCATION: http://10.0.0.8:8181/discover.json") {
			t.Fatalf("alive notification missing location: %s", payload)
		}
		if !strings.Contains(payload, "USN: uuid:tablo-homerun-proxy-12345679") {
			t.Fatalf("alive notification missing USN: %s", payload)
		}
	}
}
