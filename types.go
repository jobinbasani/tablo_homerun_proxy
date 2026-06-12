package main

type Credentials struct {
	LighthouseTVAuthorization string       `json:"lighthousetvAuthorization"`
	LighthouseTVIdentifier    string       `json:"lighthousetvIdentifier"`
	Profile                   TabloProfile `json:"profile"`
	Device                    TabloDevice  `json:"device"`
	Lighthouse                string       `json:"Lighthouse"`
	UUID                      string       `json:"UUID"`
	Tuners                    int          `json:"tuners"`
}

type TabloProfile struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

type TabloDevice struct {
	ServerID string `json:"serverId"`
	Name     string `json:"name"`
	URL      string `json:"url"`
}

type TabloAccount struct {
	Identifier string         `json:"identifier"`
	Profiles   []TabloProfile `json:"profiles"`
	Devices    []TabloDevice  `json:"devices"`
	Code       string         `json:"code"`
	Message    string         `json:"message"`
}

type LoginResponse struct {
	TokenType   string `json:"token_type"`
	AccessToken string `json:"access_token"`
	IsVerified  bool   `json:"is_verified"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

type SelectResponse struct {
	Token string `json:"token"`
}

type ServerInfo struct {
	Model struct {
		Name   string `json:"name"`
		Tuners int    `json:"tuners"`
	} `json:"model"`
}

type Channel struct {
	Identifier string        `json:"identifier"`
	Name       string        `json:"name"`
	Kind       string        `json:"kind"`
	Logos      []ChannelLogo `json:"logos"`
	OTA        ChannelKind   `json:"ota"`
	OTT        ChannelKind   `json:"ott"`
}

type ChannelLogo struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

type ChannelKind struct {
	Major     int    `json:"major"`
	Minor     int    `json:"minor"`
	CallSign  string `json:"callSign"`
	Network   string `json:"network"`
	StreamURL string `json:"streamUrl"`
}

type LineupEntry struct {
	GuideNumber string `json:"GuideNumber"`
	GuideName   string `json:"GuideName"`
	ImageURL    string `json:"ImageURL,omitempty"`
	Affiliate   string `json:"Affiliate,omitempty"`
	URL         string `json:"URL"`
	Type        string `json:"type"`
	StreamURL   string `json:"streamUrl"`
	SourceURL   string `json:"srcURL"`
}

type WatchResponse struct {
	Token       string `json:"token"`
	Expires     string `json:"expires"`
	Keepalive   int    `json:"keepalive"`
	PlaylistURL string `json:"playlist_url"`
}

type DeviceWatchRequest struct {
	Bandwidth any            `json:"bandwidth"`
	Extra     map[string]any `json:"extra"`
	DeviceID  string         `json:"device_id"`
	Platform  string         `json:"platform"`
}
