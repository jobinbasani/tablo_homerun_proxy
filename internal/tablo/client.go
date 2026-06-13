package tablo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/storage"
)

var ErrChannelNotFound = errors.New("channel not found")

type Service struct {
	cfg    config.Config
	log    *logging.Logger
	http   *http.Client
	creds  Credentials
	lineup map[string]LineupEntry
	tuners int
}

func New(cfg config.Config, logger *logging.Logger) *Service {
	return &Service{
		cfg:    cfg,
		log:    logger,
		http:   &http.Client{Timeout: 60 * time.Second},
		lineup: map[string]LineupEntry{},
		tuners: 2,
	}
}

func (s *Service) credsPath() string {
	return filepath.Join(s.cfg.OutDir, "creds.bin")
}

func (s *Service) lineupPath() string {
	return filepath.Join(s.cfg.OutDir, "lineup.json")
}

func (s *Service) GuidePath() string {
	return filepath.Join(s.cfg.OutDir, "guide.xml")
}

func (s *Service) LineupExists() bool {
	return storage.FileExists(s.lineupPath())
}

func (s *Service) GuideExists() bool {
	return storage.FileExists(s.GuidePath())
}

func (s *Service) Lineup() []LineupEntry {
	lineup := make([]LineupEntry, 0, len(s.lineup))
	for _, entry := range s.lineup {
		lineup = append(lineup, entry)
	}
	return lineup
}

func (s *Service) Channel(channelID string) (LineupEntry, bool) {
	entry, ok := s.lineup[channelID]
	return entry, ok
}

func (s *Service) TunerCount() int {
	return s.tuners
}

func (s *Service) EnsureCredentials(ctx context.Context) error {
	if s.cfg.ForceCreds || !storage.FileExists(s.credsPath()) {
		s.log.Info("No usable credentials file found. Logging into Tablo.")
		return s.CreateCredentials(ctx)
	}
	return s.ReadCredentials()
}

func (s *Service) CreateCredentials(ctx context.Context) error {
	user := s.cfg.UserName
	pass := s.cfg.UserPass
	reader := bufio.NewReader(os.Stdin)
	if user == "" {
		fmt.Print("What is your email? ")
		line, _ := reader.ReadString('\n')
		user = strings.TrimSpace(line)
	}
	if pass == "" {
		fmt.Print("What is your password? ")
		line, _ := reader.ReadString('\n')
		pass = strings.TrimSpace(line)
	}
	loginBody := map[string]string{"email": user, "password": pass}
	var login LoginResponse
	if err := s.lighthouseJSON(ctx, http.MethodPost, "/api/v2/login/", "", loginBody, &login); err != nil {
		return err
	}
	if login.AccessToken == "" || login.TokenType == "" {
		return fmt.Errorf("login was not accepted: %s", login.Message)
	}
	auth := login.TokenType + " " + login.AccessToken
	s.log.Info("Login was accepted.")

	var account TabloAccount
	if err := s.lighthouseJSON(ctx, http.MethodGet, "/api/v2/account/", auth, nil, &account); err != nil {
		return err
	}
	if account.Identifier == "" {
		return fmt.Errorf("account identifier missing")
	}
	profile, err := s.selectProfile(account.Profiles)
	if err != nil {
		return err
	}
	device, err := s.selectDevice(account.Devices)
	if err != nil {
		return err
	}

	selectBody := map[string]string{"pid": profile.Identifier, "sid": device.ServerID}
	var selected SelectResponse
	if err := s.lighthouseJSON(ctx, http.MethodPost, "/api/v2/account/select/", auth, selectBody, &selected); err != nil {
		return err
	}
	if selected.Token == "" {
		return fmt.Errorf("account token was not returned")
	}
	uuid, err := newUUID()
	if err != nil {
		return err
	}
	creds := Credentials{
		LighthouseTVAuthorization: auth,
		LighthouseTVIdentifier:    account.Identifier,
		Profile:                   profile,
		Device:                    device,
		Lighthouse:                selected.Token,
		UUID:                      uuid,
		Tuners:                    2,
	}
	var info ServerInfo
	if err := s.deviceJSON(ctx, http.MethodGet, device.URL, "/server/info", nil, &info); err != nil {
		return fmt.Errorf("could not reach Tablo device: %w", err)
	}
	if info.Model.Tuners > 0 {
		creds.Tuners = info.Model.Tuners
		s.log.Info("Found %s with %d tuners.", info.Model.Name, info.Model.Tuners)
	}
	s.creds = creds
	s.tuners = creds.Tuners
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	encrypted, err := encryptCredentials(data)
	if err != nil {
		return err
	}
	if err := storage.WriteFile(s.credsPath(), encrypted, 0o600); err != nil {
		return err
	}
	s.log.Info("Credentials encrypted at %s.", s.credsPath())
	return nil
}

func (s *Service) ReadCredentials() error {
	encrypted, err := os.ReadFile(s.credsPath())
	if err != nil {
		return err
	}
	plain, err := decryptCredentials(encrypted)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(plain, &s.creds); err != nil {
		return err
	}
	if s.creds.Tuners <= 0 {
		s.creds.Tuners = 2
	}
	s.tuners = s.creds.Tuners
	return nil
}

func (s *Service) MakeLineup(ctx context.Context) error {
	if s.creds.UUID == "" {
		if err := s.ReadCredentials(); err != nil {
			return err
		}
	}
	path := fmt.Sprintf("/api/v2/account/%s/guide/channels/", s.creds.Lighthouse)
	var channels []Channel
	if err := s.lighthouseJSON(ctx, http.MethodGet, path, s.creds.LighthouseTVAuthorization, nil, &channels); err != nil {
		return err
	}
	if !s.cfg.IncludeOTT {
		filtered := channels[:0]
		for _, channel := range channels {
			if channel.Kind != "ott" {
				filtered = append(filtered, channel)
			}
		}
		channels = filtered
	}
	if err := storage.WriteJSONFile(s.lineupPath(), channels); err != nil {
		return err
	}
	s.ParseLineup(channels)
	s.log.Info("Created channel lineup with %d channels.", len(channels))
	return nil
}

func (s *Service) LoadLineup() error {
	channels, err := storage.ReadJSONFile[[]Channel](s.lineupPath())
	if err != nil {
		return err
	}
	s.ParseLineup(channels)
	return nil
}

func (s *Service) ParseLineup(channels []Channel) {
	s.lineup = map[string]LineupEntry{}
	for _, channel := range channels {
		imageURL := bestLogo(channel.Logos)
		switch channel.Kind {
		case "ota":
			guideNumber := fmt.Sprintf("%d.%d", channel.OTA.Major, channel.OTA.Minor)
			if s.cfg.CreateXML {
				guideNumber = fmt.Sprintf("%d%d1", channel.OTA.Major, channel.OTA.Minor)
			}
			s.lineup[channel.Identifier] = LineupEntry{
				GuideNumber: guideNumber,
				GuideName:   channel.OTA.Network,
				ImageURL:    imageURL,
				Affiliate:   channel.OTA.CallSign,
				URL:         s.cfg.ServerURL + "/channel/" + url.PathEscape(channel.Identifier),
				Type:        "ota",
				StreamURL:   s.creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
				SourceURL:   s.creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
			}
		case "ott":
			if !s.cfg.IncludeOTT {
				continue
			}
			guideNumber := fmt.Sprintf("%d.%d", channel.OTT.Major, channel.OTT.Minor)
			if s.cfg.CreateXML {
				guideNumber = fmt.Sprintf("%d%d1", channel.OTT.Major, channel.OTT.Minor)
			}
			s.lineup[channel.Identifier] = LineupEntry{
				GuideNumber: guideNumber,
				GuideName:   channel.OTT.Network,
				ImageURL:    imageURL,
				Affiliate:   channel.OTT.CallSign,
				URL:         s.cfg.ServerURL + "/channel/" + url.PathEscape(channel.Identifier),
				Type:        "ott",
				StreamURL:   channel.OTT.StreamURL,
				SourceURL:   s.creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
			}
		}
	}
}

func (s *Service) Watch(ctx context.Context, channelID string) (LineupEntry, string, error) {
	entry, ok := s.Channel(channelID)
	if !ok {
		return LineupEntry{}, "", ErrChannelNotFound
	}
	body := DeviceWatchRequest{
		Bandwidth: nil,
		Extra: map[string]any{
			"limitedAdTracking": 1,
			"deviceOSVersion":   "16.6",
			"lang":              "en_US",
			"height":            1080,
			"deviceId":          "00000000-0000-0000-0000-000000000000",
			"width":             1920,
			"deviceModel":       "iPhone10,1",
			"deviceMake":        "Apple",
			"deviceOS":          "iOS",
		},
		DeviceID: s.creds.UUID,
		Platform: "ios",
	}
	var watch WatchResponse
	err := s.deviceJSON(ctx, http.MethodPost, s.creds.Device.URL, "/guide/channels/"+channelID+"/watch", body, &watch)
	if err != nil {
		return entry, "", err
	}
	if watch.PlaylistURL == "" {
		return entry, "", fmt.Errorf("playlist_url missing from Tablo response")
	}
	return entry, watch.PlaylistURL, nil
}

func (s *Service) lighthouseJSON(ctx context.Context, method, path, authorization string, body any, out any) error {
	headers := map[string]string{
		"User-Agent": "Tablo-FAST/2.0.0 (Mobile; iPhone; iOS 16.6)",
		"Accept":     "*/*",
	}
	if authorization != "" {
		headers["Authorization"] = authorization
	}
	if s.creds.Lighthouse != "" {
		headers["Lighthouse"] = s.creds.Lighthouse
	}
	return s.doJSON(ctx, method, "https://lighthousetv.ewscloud.com"+path, headers, body, out)
}

func (s *Service) deviceJSON(ctx context.Context, method, host, path string, body any, out any) error {
	payload, err := marshalBody(body)
	if err != nil {
		return err
	}
	deviceDate := time.Now().UTC().Format(http.TimeFormat)
	headers := map[string]string{
		"Connection":    "keep-alive",
		"Date":          deviceDate,
		"Accept":        "*/*",
		"User-Agent":    "Tablo-FAST/1.7.0 (Mobile; iPhone; iOS 18.4)",
		"Authorization": makeDeviceAuth(method, path, payload, deviceDate),
	}
	if len(payload) > 0 {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	return s.doRawJSON(ctx, method, host+path, headers, payload, out)
}

func (s *Service) doJSON(ctx context.Context, method, endpoint string, headers map[string]string, body any, out any) error {
	payload, err := marshalBody(body)
	if err != nil {
		return err
	}
	if len(payload) > 0 {
		headers["Content-Type"] = "application/json"
	}
	return s.doRawJSON(ctx, method, endpoint, headers, payload, out)
}

func (s *Service) doRawJSON(ctx context.Context, method, endpoint string, headers map[string]string, payload []byte, out any) error {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s failed with %s: %s", endpoint, resp.Status, string(data))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func marshalBody(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	return json.Marshal(body)
}

func (s *Service) selectProfile(profiles []TabloProfile) (TabloProfile, error) {
	if len(profiles) == 0 {
		return TabloProfile{}, fmt.Errorf("no Tablo profiles found")
	}
	if len(profiles) == 1 || s.cfg.UserName != "" {
		s.log.Info("Using profile %s.", profiles[0].Name)
		return profiles[0], nil
	}
	for index, profile := range profiles {
		fmt.Printf("%d) %s\n", index+1, profile.Name)
	}
	index := promptIndex("Select profile", len(profiles))
	return profiles[index], nil
}

func (s *Service) selectDevice(devices []TabloDevice) (TabloDevice, error) {
	if len(devices) == 0 {
		return TabloDevice{}, fmt.Errorf("no Tablo devices found")
	}
	if s.cfg.TabloDevice != "" {
		for _, device := range devices {
			if device.ServerID == s.cfg.TabloDevice {
				s.log.Info("Using device %s %s @ %s.", device.Name, device.ServerID, device.URL)
				return device, nil
			}
		}
		return TabloDevice{}, fmt.Errorf("device %s was not found", s.cfg.TabloDevice)
	}
	if len(devices) == 1 || s.cfg.UserName != "" {
		s.log.Info("Using device %s %s @ %s.", devices[0].Name, devices[0].ServerID, devices[0].URL)
		return devices[0], nil
	}
	for index, device := range devices {
		fmt.Printf("%d) %s %s @ %s\n", index+1, device.Name, device.ServerID, device.URL)
	}
	index := promptIndex("Select device", len(devices))
	return devices[index], nil
}

func promptIndex(label string, max int) int {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s [1-%d]: ", label, max)
		line, _ := reader.ReadString('\n')
		var choice int
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &choice); err == nil && choice >= 1 && choice <= max {
			return choice - 1
		}
	}
}

func bestLogo(logos []ChannelLogo) string {
	if len(logos) == 0 {
		return ""
	}
	for _, logo := range logos {
		if logo.Kind == "lightLarge" {
			return logo.URL
		}
	}
	return logos[0].URL
}
