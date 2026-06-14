package tablo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/logging"
	"github.com/jobinbasani/tablo_homerun_proxy/internal/storage"
)

var ErrChannelNotFound = errors.New("channel not found")
var ErrCredentialsMissing = errors.New("tablo credentials are not configured")

type Service struct {
	cfg             config.Config
	log             *logging.Logger
	credentialStore CredentialStore
	http            *http.Client
	creds           Credentials
	lineup          map[string]LineupEntry
	tuners          int
	setup           *pendingSetup
	mu              sync.RWMutex
}

type CredentialStore interface {
	SaveTabloCredentials(context.Context, []byte) error
	LoadTabloCredentials(context.Context) ([]byte, error)
	HasTabloCredentials(context.Context) (bool, error)
}

type pendingSetup struct {
	authorization string
	account       TabloAccount
	profile       TabloProfile
}

func New(cfg config.Config, logger *logging.Logger, credentialStore CredentialStore) *Service {
	return &Service{
		cfg:             cfg,
		log:             logger,
		credentialStore: credentialStore,
		http:            &http.Client{Timeout: 60 * time.Second},
		lineup:          map[string]LineupEntry{},
		tuners:          2,
	}
}

func (s *Service) lineupPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return filepath.Join(s.cfg.OutDir, "lineup.json")
}

func (s *Service) GuidePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return filepath.Join(s.cfg.OutDir, "guide.xml")
}

func (s *Service) SetConfig(cfg config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

func (s *Service) Config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Service) LineupExists() bool {
	return storage.FileExists(s.lineupPath())
}

func (s *Service) GuideExists() bool {
	return storage.FileExists(s.GuidePath())
}

func (s *Service) Lineup() []LineupEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lineup := make([]LineupEntry, 0, len(s.lineup))
	for _, entry := range s.lineup {
		lineup = append(lineup, entry)
	}
	return lineup
}

func (s *Service) Channel(channelID string) (LineupEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.lineup[channelID]
	return entry, ok
}

func (s *Service) TunerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tuners
}

func (s *Service) EnsureCredentialsNonInteractive(ctx context.Context) error {
	cfg := s.Config()
	hasCredentials, err := s.credentialStore.HasTabloCredentials(ctx)
	if err != nil {
		return err
	}
	if hasCredentials && !cfg.ForceCreds {
		return s.ReadCredentials(ctx)
	}
	if cfg.UserName == "" || cfg.UserPass == "" {
		return ErrCredentialsMissing
	}
	auth, account, err := s.loginAccount(ctx, cfg.UserName, cfg.UserPass)
	if err != nil {
		return err
	}
	profile, err := s.selectProfile(account.Profiles)
	if err != nil {
		return err
	}
	device, err := s.selectDevice(account.Devices)
	if err != nil {
		return err
	}
	return s.createCredentialsForSelection(ctx, auth, account.Identifier, profile, device)
}

func (s *Service) LoginForDevices(ctx context.Context, user, pass string) ([]TabloDevice, error) {
	auth, account, err := s.loginAccount(ctx, user, pass)
	if err != nil {
		return nil, err
	}
	profile, err := s.selectProfile(account.Profiles)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.setup = &pendingSetup{authorization: auth, account: account, profile: profile}
	s.mu.Unlock()
	return account.Devices, nil
}

func (s *Service) SelectDevice(ctx context.Context, serverID string) error {
	s.mu.RLock()
	setup := s.setup
	s.mu.RUnlock()
	if setup == nil {
		return fmt.Errorf("no pending Tablo login")
	}
	for _, device := range setup.account.Devices {
		if device.ServerID == serverID {
			return s.createCredentialsForSelection(ctx, setup.authorization, setup.account.Identifier, setup.profile, device)
		}
	}
	return fmt.Errorf("device %s was not found", serverID)
}

func (s *Service) loginAccount(ctx context.Context, user, pass string) (string, TabloAccount, error) {
	loginBody := map[string]string{"email": user, "password": pass}
	var login LoginResponse
	if err := s.lighthouseJSON(ctx, http.MethodPost, "/api/v2/login/", "", loginBody, &login); err != nil {
		return "", TabloAccount{}, err
	}
	if login.AccessToken == "" || login.TokenType == "" {
		return "", TabloAccount{}, fmt.Errorf("login was not accepted: %s", login.Message)
	}
	auth := login.TokenType + " " + login.AccessToken
	s.log.Info("Login was accepted.")

	var account TabloAccount
	if err := s.lighthouseJSON(ctx, http.MethodGet, "/api/v2/account/", auth, nil, &account); err != nil {
		return "", TabloAccount{}, err
	}
	if account.Identifier == "" {
		return "", TabloAccount{}, fmt.Errorf("account identifier missing")
	}
	return auth, account, nil
}

func (s *Service) createCredentialsForSelection(ctx context.Context, auth, accountID string, profile TabloProfile, device TabloDevice) error {
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
		LighthouseTVIdentifier:    accountID,
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
	s.mu.Lock()
	s.creds = creds
	s.tuners = creds.Tuners
	s.setup = nil
	s.mu.Unlock()
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	encrypted, err := encryptCredentials(data)
	if err != nil {
		return err
	}
	if err := s.credentialStore.SaveTabloCredentials(ctx, encrypted); err != nil {
		return err
	}
	s.log.Info("Credentials encrypted in the application database.")
	return nil
}

func (s *Service) ReadCredentials(ctx context.Context) error {
	encrypted, err := s.credentialStore.LoadTabloCredentials(ctx)
	if err != nil {
		return err
	}
	plain, err := decryptCredentials(encrypted)
	if err != nil {
		return err
	}
	var creds Credentials
	if err := json.Unmarshal(plain, &creds); err != nil {
		return err
	}
	if creds.Tuners <= 0 {
		creds.Tuners = 2
	}
	s.mu.Lock()
	s.creds = creds
	s.tuners = creds.Tuners
	s.mu.Unlock()
	return nil
}

func (s *Service) MakeLineup(ctx context.Context) error {
	s.mu.RLock()
	creds := s.creds
	includeOTT := s.cfg.IncludeOTT
	s.mu.RUnlock()
	if creds.UUID == "" {
		if err := s.ReadCredentials(ctx); err != nil {
			return err
		}
		s.mu.RLock()
		creds = s.creds
		includeOTT = s.cfg.IncludeOTT
		s.mu.RUnlock()
	}
	path := fmt.Sprintf("/api/v2/account/%s/guide/channels/", creds.Lighthouse)
	var channels []Channel
	if err := s.lighthouseJSON(ctx, http.MethodGet, path, creds.LighthouseTVAuthorization, nil, &channels); err != nil {
		return err
	}
	if !includeOTT {
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
	cfg := s.Config()
	s.mu.RLock()
	creds := s.creds
	s.mu.RUnlock()
	lineup := map[string]LineupEntry{}
	for _, channel := range channels {
		imageURL := bestLogo(channel.Logos)
		switch channel.Kind {
		case "ota":
			guideNumber := fmt.Sprintf("%d.%d", channel.OTA.Major, channel.OTA.Minor)
			if cfg.CreateXML {
				guideNumber = fmt.Sprintf("%d%d1", channel.OTA.Major, channel.OTA.Minor)
			}
			lineup[channel.Identifier] = LineupEntry{
				GuideNumber: guideNumber,
				GuideName:   channel.OTA.Network,
				ImageURL:    imageURL,
				Affiliate:   channel.OTA.CallSign,
				URL:         cfg.ServerURL + "/channel/" + url.PathEscape(channel.Identifier),
				Type:        "ota",
				StreamURL:   creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
				SourceURL:   creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
			}
		case "ott":
			if !cfg.IncludeOTT {
				continue
			}
			guideNumber := fmt.Sprintf("%d.%d", channel.OTT.Major, channel.OTT.Minor)
			if cfg.CreateXML {
				guideNumber = fmt.Sprintf("%d%d1", channel.OTT.Major, channel.OTT.Minor)
			}
			lineup[channel.Identifier] = LineupEntry{
				GuideNumber: guideNumber,
				GuideName:   channel.OTT.Network,
				ImageURL:    imageURL,
				Affiliate:   channel.OTT.CallSign,
				URL:         cfg.ServerURL + "/channel/" + url.PathEscape(channel.Identifier),
				Type:        "ott",
				StreamURL:   channel.OTT.StreamURL,
				SourceURL:   creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
			}
		}
	}
	s.mu.Lock()
	s.lineup = lineup
	s.mu.Unlock()
}

func (s *Service) Watch(ctx context.Context, channelID string) (LineupEntry, string, error) {
	entry, ok := s.Channel(channelID)
	if !ok {
		return LineupEntry{}, "", ErrChannelNotFound
	}
	s.mu.RLock()
	creds := s.creds
	s.mu.RUnlock()
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
		DeviceID: creds.UUID,
		Platform: "ios",
	}
	var watch WatchResponse
	err := s.deviceJSON(ctx, http.MethodPost, creds.Device.URL, "/guide/channels/"+channelID+"/watch", body, &watch)
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
	s.log.Info("Using profile %s.", profiles[0].Name)
	return profiles[0], nil
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
	s.log.Info("Using device %s %s @ %s.", devices[0].Name, devices[0].ServerID, devices[0].URL)
	return devices[0], nil
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
