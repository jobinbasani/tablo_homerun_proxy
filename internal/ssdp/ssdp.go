package ssdp

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
)

const (
	multicastAddress = "239.255.255.250:1900"
	maxPacketSize    = 2048
	notifyInterval   = 30 * time.Second
	cacheControl     = "max-age=1800"
	serverHeader     = "tablo-homerun-proxy/1.0 UPnP/1.1 HDHomeRun/1.0"
)

type ConfigProvider func() config.Config
type ReadyProvider func() bool

type Service struct {
	cfg   ConfigProvider
	ready ReadyProvider
	log   *logging.Logger
}

func New(cfg ConfigProvider, ready ReadyProvider, logger *logging.Logger) *Service {
	return &Service{cfg: cfg, ready: ready, log: logger}
}

func (s *Service) Run(ctx context.Context) {
	addr, err := net.ResolveUDPAddr("udp4", multicastAddress)
	if err != nil {
		s.log.Warn("SSDP disabled: could not resolve multicast address: %v", err)
		return
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		s.log.Warn("SSDP disabled: could not listen on UDP 1900: %v", err)
		return
	}
	defer conn.Close()
	if err := conn.SetReadBuffer(maxPacketSize); err != nil {
		s.log.Warn("SSDP read buffer setup failed: %v", err)
	}
	s.log.Always("SSDP discovery is listening on udp://%s.", multicastAddress)

	go s.announceLoop(ctx, conn, addr)

	buf := make([]byte, maxPacketSize)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			s.log.Warn("SSDP read deadline failed: %v", err)
			return
		}
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				select {
				case <-ctx.Done():
					s.sendByebye(conn, addr)
					return
				default:
					continue
				}
			}
			s.log.Warn("SSDP read failed: %v", err)
			continue
		}
		if !s.ready() {
			continue
		}
		if response, ok := SearchResponse(s.cfg(), string(buf[:n])); ok {
			if _, err := conn.WriteToUDP([]byte(response), remote); err != nil {
				s.log.Warn("SSDP response failed: %v", err)
			}
		}
	}
}

func (s *Service) announceLoop(ctx context.Context, conn *net.UDPConn, addr *net.UDPAddr) {
	ticker := time.NewTicker(notifyInterval)
	defer ticker.Stop()
	s.sendAlive(conn, addr)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sendAlive(conn, addr)
		}
	}
}

func (s *Service) sendAlive(conn *net.UDPConn, addr *net.UDPAddr) {
	if !s.ready() {
		return
	}
	for _, payload := range AliveNotifications(s.cfg()) {
		if _, err := conn.WriteToUDP([]byte(payload), addr); err != nil {
			s.log.Warn("SSDP alive notification failed: %v", err)
			return
		}
	}
}

func (s *Service) sendByebye(conn *net.UDPConn, addr *net.UDPAddr) {
	if !s.ready() {
		return
	}
	for _, payload := range ByebyeNotifications(s.cfg()) {
		_, _ = conn.WriteToUDP([]byte(payload), addr)
	}
}

func SearchResponse(cfg config.Config, request string) (string, bool) {
	if !isMSearch(request) || !supportedSearchTarget(request) {
		return "", false
	}
	usn := usn(cfg)
	return strings.Join([]string{
		"HTTP/1.1 200 OK",
		"CACHE-CONTROL: " + cacheControl,
		"EXT:",
		"LOCATION: " + discoverURL(cfg),
		"SERVER: " + serverHeader,
		"ST: " + searchTarget(request),
		"USN: " + usn,
		"",
		"",
	}, "\r\n"), true
}

func AliveNotifications(cfg config.Config) []string {
	return notifications(cfg, "ssdp:alive")
}

func ByebyeNotifications(cfg config.Config) []string {
	return notifications(cfg, "ssdp:byebye")
}

func notifications(cfg config.Config, nts string) []string {
	types := []string{"upnp:rootdevice", "urn:schemas-upnp-org:device:MediaServer:1", "urn:schemas-upnp-org:device:HDHomeRun:1"}
	messages := make([]string, 0, len(types))
	for _, nt := range types {
		lines := []string{
			"NOTIFY * HTTP/1.1",
			"HOST: " + multicastAddress,
			"CACHE-CONTROL: " + cacheControl,
			"LOCATION: " + discoverURL(cfg),
			"NT: " + nt,
			"NTS: " + nts,
			"SERVER: " + serverHeader,
			"USN: " + usn(cfg) + "::" + nt,
			"",
			"",
		}
		messages = append(messages, strings.Join(lines, "\r\n"))
	}
	return messages
}

func isMSearch(request string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimLeft(request, "\r\n\t ")), "M-SEARCH")
}

func supportedSearchTarget(request string) bool {
	target := strings.ToLower(searchTarget(request))
	if target == "" {
		return false
	}
	return target == "ssdp:all" ||
		target == "upnp:rootdevice" ||
		strings.Contains(target, "hdhomerun") ||
		strings.Contains(target, "mediaserver")
}

func searchTarget(request string) string {
	for _, line := range strings.Split(request, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "ST") {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

func discoverURL(cfg config.Config) string {
	base := strings.TrimRight(cfg.ServerURL, "/")
	if _, err := url.ParseRequestURI(base); err != nil {
		return fmt.Sprintf("http://%s:%s/discover.json", cfg.IPAddress, cfg.Port)
	}
	return base + "/discover.json"
}

func usn(cfg config.Config) string {
	deviceID := strings.TrimSpace(cfg.DeviceID)
	if deviceID == "" {
		deviceID = "12345679"
	}
	return "uuid:tablo-homerun-proxy-" + deviceID
}
