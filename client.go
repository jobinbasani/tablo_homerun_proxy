package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type App struct {
	cfg       Config
	log       *Logger
	http      *http.Client
	creds     Credentials
	lineup    map[string]LineupEntry
	tuners    int
	streams   int64
	streamSem chan struct{}
}

func NewApp(cfg Config, logger *Logger) *App {
	return &App{
		cfg:       cfg,
		log:       logger,
		http:      &http.Client{Timeout: 60 * time.Second},
		lineup:    map[string]LineupEntry{},
		tuners:    2,
		streamSem: make(chan struct{}, 2),
	}
}

func (a *App) credsPath() string {
	return filepath.Join(a.cfg.OutDir, "creds.bin")
}

func (a *App) lineupPath() string {
	return filepath.Join(a.cfg.OutDir, "lineup.json")
}

func (a *App) guidePath() string {
	return filepath.Join(a.cfg.OutDir, "guide.xml")
}

func (a *App) EnsureCredentials(ctx context.Context) error {
	if a.cfg.ForceCreds || !fileExists(a.credsPath()) {
		a.log.Info("No usable credentials file found. Logging into Tablo.")
		return a.CreateCredentials(ctx)
	}
	return a.ReadCredentials()
}

func (a *App) CreateCredentials(ctx context.Context) error {
	user := a.cfg.UserName
	pass := a.cfg.UserPass
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
	if err := a.lighthouseJSON(ctx, http.MethodPost, "/api/v2/login/", "", loginBody, &login); err != nil {
		return err
	}
	if login.AccessToken == "" || login.TokenType == "" {
		return fmt.Errorf("login was not accepted: %s", login.Message)
	}
	auth := login.TokenType + " " + login.AccessToken
	a.log.Info("Login was accepted.")

	var account TabloAccount
	if err := a.lighthouseJSON(ctx, http.MethodGet, "/api/v2/account/", auth, nil, &account); err != nil {
		return err
	}
	if account.Identifier == "" {
		return fmt.Errorf("account identifier missing")
	}
	profile, err := a.selectProfile(account.Profiles)
	if err != nil {
		return err
	}
	device, err := a.selectDevice(account.Devices)
	if err != nil {
		return err
	}

	selectBody := map[string]string{"pid": profile.Identifier, "sid": device.ServerID}
	var selected SelectResponse
	if err := a.lighthouseJSON(ctx, http.MethodPost, "/api/v2/account/select/", auth, selectBody, &selected); err != nil {
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
	if err := a.deviceJSON(ctx, http.MethodGet, device.URL, "/server/info", nil, &info); err != nil {
		return fmt.Errorf("could not reach Tablo device: %w", err)
	}
	if info.Model.Tuners > 0 {
		creds.Tuners = info.Model.Tuners
		a.log.Info("Found %s with %d tuners.", info.Model.Name, info.Model.Tuners)
	}
	a.creds = creds
	a.tuners = creds.Tuners
	a.streamSem = make(chan struct{}, a.tuners)
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	encrypted, err := encryptCredentials(data)
	if err != nil {
		return err
	}
	if err := writeFile(a.credsPath(), encrypted, 0o600); err != nil {
		return err
	}
	a.log.Info("Credentials encrypted at %s.", a.credsPath())
	return nil
}

func (a *App) ReadCredentials() error {
	encrypted, err := os.ReadFile(a.credsPath())
	if err != nil {
		return err
	}
	plain, err := decryptCredentials(encrypted)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(plain, &a.creds); err != nil {
		return err
	}
	if a.creds.Tuners <= 0 {
		a.creds.Tuners = 2
	}
	a.tuners = a.creds.Tuners
	a.streamSem = make(chan struct{}, a.tuners)
	return nil
}

func (a *App) MakeLineup(ctx context.Context) error {
	if a.creds.UUID == "" {
		if err := a.ReadCredentials(); err != nil {
			return err
		}
	}
	path := fmt.Sprintf("/api/v2/account/%s/guide/channels/", a.creds.Lighthouse)
	var channels []Channel
	if err := a.lighthouseJSON(ctx, http.MethodGet, path, a.creds.LighthouseTVAuthorization, nil, &channels); err != nil {
		return err
	}
	if !a.cfg.IncludeOTT {
		filtered := channels[:0]
		for _, channel := range channels {
			if channel.Kind != "ott" {
				filtered = append(filtered, channel)
			}
		}
		channels = filtered
	}
	if err := writeJSONFile(a.lineupPath(), channels); err != nil {
		return err
	}
	a.ParseLineup(channels)
	a.log.Info("Created channel lineup with %d channels.", len(channels))
	return nil
}

func (a *App) LoadLineup() error {
	channels, err := readJSONFile[[]Channel](a.lineupPath())
	if err != nil {
		return err
	}
	a.ParseLineup(channels)
	return nil
}

func (a *App) ParseLineup(channels []Channel) {
	a.lineup = map[string]LineupEntry{}
	for _, channel := range channels {
		imageURL := bestLogo(channel.Logos)
		switch channel.Kind {
		case "ota":
			guideNumber := fmt.Sprintf("%d.%d", channel.OTA.Major, channel.OTA.Minor)
			if a.cfg.CreateXML {
				guideNumber = fmt.Sprintf("%d%d1", channel.OTA.Major, channel.OTA.Minor)
			}
			a.lineup[channel.Identifier] = LineupEntry{
				GuideNumber: guideNumber,
				GuideName:   channel.OTA.Network,
				ImageURL:    imageURL,
				Affiliate:   channel.OTA.CallSign,
				URL:         a.cfg.ServerURL + "/channel/" + url.PathEscape(channel.Identifier),
				Type:        "ota",
				StreamURL:   a.creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
				SourceURL:   a.creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
			}
		case "ott":
			if !a.cfg.IncludeOTT {
				continue
			}
			guideNumber := fmt.Sprintf("%d.%d", channel.OTT.Major, channel.OTT.Minor)
			if a.cfg.CreateXML {
				guideNumber = fmt.Sprintf("%d%d1", channel.OTT.Major, channel.OTT.Minor)
			}
			a.lineup[channel.Identifier] = LineupEntry{
				GuideNumber: guideNumber,
				GuideName:   channel.OTT.Network,
				ImageURL:    imageURL,
				Affiliate:   channel.OTT.CallSign,
				URL:         a.cfg.ServerURL + "/channel/" + url.PathEscape(channel.Identifier),
				Type:        "ott",
				StreamURL:   channel.OTT.StreamURL,
				SourceURL:   a.creds.Device.URL + "/guide/channels/" + channel.Identifier + "/watch",
			}
		}
	}
}

func (a *App) lighthouseJSON(ctx context.Context, method, path, authorization string, body any, out any) error {
	headers := map[string]string{
		"User-Agent": "Tablo-FAST/2.0.0 (Mobile; iPhone; iOS 16.6)",
		"Accept":     "*/*",
	}
	if authorization != "" {
		headers["Authorization"] = authorization
	}
	if a.creds.Lighthouse != "" {
		headers["Lighthouse"] = a.creds.Lighthouse
	}
	return a.doJSON(ctx, method, "https://lighthousetv.ewscloud.com"+path, headers, body, out)
}

func (a *App) deviceJSON(ctx context.Context, method, host, path string, body any, out any) error {
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
	return a.doRawJSON(ctx, method, host+path, headers, payload, out)
}

func (a *App) doJSON(ctx context.Context, method, endpoint string, headers map[string]string, body any, out any) error {
	payload, err := marshalBody(body)
	if err != nil {
		return err
	}
	if len(payload) > 0 {
		headers["Content-Type"] = "application/json"
	}
	return a.doRawJSON(ctx, method, endpoint, headers, payload, out)
}

func (a *App) doRawJSON(ctx context.Context, method, endpoint string, headers map[string]string, payload []byte, out any) error {
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
	resp, err := a.http.Do(req)
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

func (a *App) selectProfile(profiles []TabloProfile) (TabloProfile, error) {
	if len(profiles) == 0 {
		return TabloProfile{}, fmt.Errorf("no Tablo profiles found")
	}
	if len(profiles) == 1 || a.cfg.UserName != "" {
		a.log.Info("Using profile %s.", profiles[0].Name)
		return profiles[0], nil
	}
	for index, profile := range profiles {
		fmt.Printf("%d) %s\n", index+1, profile.Name)
	}
	index := promptIndex("Select profile", len(profiles))
	return profiles[index], nil
}

func (a *App) selectDevice(devices []TabloDevice) (TabloDevice, error) {
	if len(devices) == 0 {
		return TabloDevice{}, fmt.Errorf("no Tablo devices found")
	}
	if a.cfg.TabloDevice != "" {
		for _, device := range devices {
			if device.ServerID == a.cfg.TabloDevice {
				a.log.Info("Using device %s %s @ %s.", device.Name, device.ServerID, device.URL)
				return device, nil
			}
		}
		return TabloDevice{}, fmt.Errorf("device %s was not found", a.cfg.TabloDevice)
	}
	if len(devices) == 1 || a.cfg.UserName != "" {
		a.log.Info("Using device %s %s @ %s.", devices[0].Name, devices[0].ServerID, devices[0].URL)
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
