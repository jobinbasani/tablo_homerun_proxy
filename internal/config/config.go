package config

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	Name                 string
	DeviceID             string
	Port                 string
	LineupInterval       time.Duration
	CreateXML            bool
	GuideDays            int
	IncludePseudoTVGuide bool
	LogLevel             string
	OutDir               string
	TabloDevice          string
	UserName             string
	UserPass             string
	IPAddress            string
	GuideInterval        time.Duration
	IncludeOTT           bool
	FFmpegLogLevel       string
	EnvPath              string
	ServerURL            string
	ForceCreds           bool
	ForceLineup          bool
	DBPath               string
	AdminPassword        string
	Explicit             map[string]bool `json:"-"`
}

type EnvConfig struct {
	Name                 string `envconfig:"NAME" default:"Tablo 4th Gen Proxy"`
	DeviceID             string `envconfig:"DEVICE_ID" default:"12345679"`
	Port                 string `envconfig:"PORT" default:"8181"`
	LineupUpdateInterval int    `envconfig:"LINEUP_UPDATE_INTERVAL" default:"30"`
	CreateXML            bool   `envconfig:"CREATE_XML" default:"false"`
	GuideDays            int    `envconfig:"GUIDE_DAYS" default:"2"`
	IncludePseudoTVGuide bool   `envconfig:"INCLUDE_PSEUDOTV_GUIDE" default:"false"`
	LogLevel             string `envconfig:"LOG_LEVEL" default:"info"`
	OutDir               string `envconfig:"OUT_DIR" default:""`
	TabloDevice          string `envconfig:"TABLO_DEVICE" default:""`
	UserName             string `envconfig:"USER_NAME" default:""`
	UserPass             string `envconfig:"USER_PASS" default:""`
	IPAddress            string `envconfig:"IP_ADDRESS" default:""`
	GuideUpdateInterval  int    `envconfig:"GUIDE_UPDATE_INTERVAL" default:"24"`
	IncludeOTT           bool   `envconfig:"INCLUDE_OTT" default:"true"`
	DBPath               string `envconfig:"DB_PATH" default:""`
	AdminPassword        string `envconfig:"ADMIN_PASSWORD" default:""`
}

func Load() (Config, error) {
	baseDir, err := os.Getwd()
	if err != nil {
		return Config{}, err
	}
	explicit := explicitEnv()
	envPath := filepath.Join(baseDir, ".env")
	if err := ensureEnvFile(envPath); err != nil {
		return Config{}, err
	}
	env, err := readEnvFile(envPath)
	if err != nil {
		return Config{}, err
	}
	for key, value := range env {
		if defaultValue, ok := defaultEnvValues()[key]; ok && value != defaultValue {
			explicit[envFieldName(key)] = true
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}

	envCfg := EnvConfig{}
	if err := envconfig.Process("", &envCfg); err != nil {
		return Config{}, err
	}

	cfg := Config{}
	flag.BoolVar(&cfg.ForceCreds, "creds", false, "force recreation of Tablo credentials")
	flag.BoolVar(&cfg.ForceCreds, "c", false, "force recreation of Tablo credentials")
	flag.BoolVar(&cfg.ForceLineup, "lineup", false, "force creation of a fresh lineup file")
	flag.BoolVar(&cfg.ForceLineup, "l", false, "force creation of a fresh lineup file")

	name := flag.String("name", envCfg.Name, "device name shown to Plex")
	deviceID := flag.String("id", envCfg.DeviceID, "fake HDHomeRun device ID")
	port := flag.String("port", envCfg.Port, "HTTP port")
	lineupDays := flag.Int("channels", envCfg.LineupUpdateInterval, "lineup update interval in days")
	createXML := flag.Bool("xml", envCfg.CreateXML, "create XMLTV guide data")
	guideDays := flag.Int("days", envCfg.GuideDays, "guide days to cache")
	pseudo := flag.Bool("pseudo", envCfg.IncludePseudoTVGuide, "include .pseudotv/xmltv.xml")
	logLevel := flag.String("level", envCfg.LogLevel, "log level: info,error,warn,debug")
	outDir := flag.String("outdir", envCfg.OutDir, "output directory")
	tabloDevice := flag.String("device", envCfg.TabloDevice, "Tablo server ID")
	user := flag.String("user", envCfg.UserName, "Tablo username")
	pass := flag.String("pass", envCfg.UserPass, "Tablo password")
	ip := flag.String("ip_address", envCfg.IPAddress, "static IP address for advertised server URL")
	guideHours := flag.Int("guide", envCfg.GuideUpdateInterval, "guide update interval in hours")
	ott := flag.Bool("ott", envCfg.IncludeOTT, "include OTT channels")
	dbPath := flag.String("db", envCfg.DBPath, "SQLite database path")
	adminPassword := flag.String("admin_password", envCfg.AdminPassword, "initial admin password")
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		if field := flagFieldName(f.Name); field != "" {
			explicit[field] = true
		}
	})

	cfg.Name = *name
	cfg.DeviceID = *deviceID
	cfg.Port = *port
	cfg.LineupInterval = time.Duration(*lineupDays) * 24 * time.Hour
	cfg.CreateXML = *createXML
	cfg.GuideDays = clamp(*guideDays, 1, 7)
	cfg.IncludePseudoTVGuide = *pseudo
	cfg.LogLevel = normalizeLogLevel(*logLevel)
	cfg.TabloDevice = *tabloDevice
	cfg.UserName = *user
	cfg.UserPass = *pass
	cfg.GuideInterval = time.Duration(*guideHours) * time.Hour
	cfg.IncludeOTT = *ott
	cfg.EnvPath = envPath
	if *outDir == "" {
		cfg.OutDir = baseDir
	} else {
		cfg.OutDir = *outDir
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return Config{}, err
	}
	cfg.DBPath = *dbPath
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.OutDir, "proxy.db")
	}
	cfg.AdminPassword = *adminPassword
	cfg.Explicit = explicit
	if *ip == "" {
		cfg.IPAddress = firstIPv4()
	} else {
		cfg.IPAddress = *ip
	}
	cfg = ApplyDerived(cfg)
	return cfg, nil
}

func ApplyStartupOverrides(stored Config, boot Config) (Config, bool) {
	next := stored
	changed := false
	applyString := func(field string, target *string, value string) {
		if boot.Explicit[field] && *target != value {
			*target = value
			changed = true
		}
	}
	applyBool := func(field string, target *bool, value bool) {
		if boot.Explicit[field] && *target != value {
			*target = value
			changed = true
		}
	}
	applyInt := func(field string, target *int, value int) {
		if boot.Explicit[field] && *target != value {
			*target = value
			changed = true
		}
	}
	applyDuration := func(field string, target *time.Duration, value time.Duration) {
		if boot.Explicit[field] && *target != value {
			*target = value
			changed = true
		}
	}

	applyString("Name", &next.Name, boot.Name)
	applyString("DeviceID", &next.DeviceID, boot.DeviceID)
	applyString("Port", &next.Port, boot.Port)
	applyDuration("LineupInterval", &next.LineupInterval, boot.LineupInterval)
	applyBool("CreateXML", &next.CreateXML, boot.CreateXML)
	applyInt("GuideDays", &next.GuideDays, boot.GuideDays)
	applyBool("IncludePseudoTVGuide", &next.IncludePseudoTVGuide, boot.IncludePseudoTVGuide)
	applyString("LogLevel", &next.LogLevel, boot.LogLevel)
	applyString("OutDir", &next.OutDir, boot.OutDir)
	applyString("TabloDevice", &next.TabloDevice, boot.TabloDevice)
	applyString("IPAddress", &next.IPAddress, boot.IPAddress)
	applyDuration("GuideInterval", &next.GuideInterval, boot.GuideInterval)
	applyBool("IncludeOTT", &next.IncludeOTT, boot.IncludeOTT)

	return ApplyDerived(next), changed
}

func ApplyDerived(cfg Config) Config {
	if cfg.IPAddress == "" {
		cfg.IPAddress = firstIPv4()
	}
	cfg.GuideDays = clamp(cfg.GuideDays, 1, 7)
	cfg.LogLevel = normalizeLogLevel(cfg.LogLevel)
	cfg.ServerURL = fmt.Sprintf("http://%s:%s", cfg.IPAddress, cfg.Port)
	cfg.FFmpegLogLevel = ffmpegLogLevel(cfg.LogLevel)
	return cfg
}

func ensureEnvFile(path string) error {
	defaults := []string{}
	for _, key := range defaultEnvOrder() {
		defaults = append(defaults, fmt.Sprintf(`%s="%s"`, key, defaultEnvValues()[key]))
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.WriteFile(path, []byte(strings.Join(defaults, "\n")+"\n"), 0o600)
	}
	return nil
}

func explicitEnv() map[string]bool {
	explicit := map[string]bool{}
	for key := range defaultEnvValues() {
		if _, ok := os.LookupEnv(key); ok {
			if field := envFieldName(key); field != "" {
				explicit[field] = true
			}
		}
	}
	return explicit
}

func defaultEnvOrder() []string {
	return []string{
		"NAME", "DEVICE_ID", "PORT", "LINEUP_UPDATE_INTERVAL", "CREATE_XML", "GUIDE_DAYS",
		"INCLUDE_PSEUDOTV_GUIDE", "LOG_LEVEL", "OUT_DIR", "TABLO_DEVICE", "USER_NAME",
		"USER_PASS", "IP_ADDRESS", "GUIDE_UPDATE_INTERVAL", "INCLUDE_OTT", "DB_PATH", "ADMIN_PASSWORD",
	}
}

func defaultEnvValues() map[string]string {
	return map[string]string{
		"NAME":                   "Tablo 4th Gen Proxy",
		"DEVICE_ID":              "12345679",
		"PORT":                   "8181",
		"LINEUP_UPDATE_INTERVAL": "30",
		"CREATE_XML":             "false",
		"GUIDE_DAYS":             "2",
		"INCLUDE_PSEUDOTV_GUIDE": "false",
		"LOG_LEVEL":              "info",
		"OUT_DIR":                "",
		"TABLO_DEVICE":           "",
		"USER_NAME":              "",
		"USER_PASS":              "",
		"IP_ADDRESS":             "",
		"GUIDE_UPDATE_INTERVAL":  "24",
		"INCLUDE_OTT":            "true",
		"DB_PATH":                "",
		"ADMIN_PASSWORD":         "",
	}
}

func envFieldName(key string) string {
	switch key {
	case "NAME":
		return "Name"
	case "DEVICE_ID":
		return "DeviceID"
	case "PORT":
		return "Port"
	case "LINEUP_UPDATE_INTERVAL":
		return "LineupInterval"
	case "CREATE_XML":
		return "CreateXML"
	case "GUIDE_DAYS":
		return "GuideDays"
	case "INCLUDE_PSEUDOTV_GUIDE":
		return "IncludePseudoTVGuide"
	case "LOG_LEVEL":
		return "LogLevel"
	case "OUT_DIR":
		return "OutDir"
	case "TABLO_DEVICE":
		return "TabloDevice"
	case "USER_NAME":
		return "UserName"
	case "USER_PASS":
		return "UserPass"
	case "IP_ADDRESS":
		return "IPAddress"
	case "GUIDE_UPDATE_INTERVAL":
		return "GuideInterval"
	case "INCLUDE_OTT":
		return "IncludeOTT"
	case "DB_PATH":
		return "DBPath"
	case "ADMIN_PASSWORD":
		return "AdminPassword"
	default:
		return ""
	}
}

func flagFieldName(name string) string {
	switch name {
	case "name":
		return "Name"
	case "id":
		return "DeviceID"
	case "port":
		return "Port"
	case "channels":
		return "LineupInterval"
	case "xml":
		return "CreateXML"
	case "days":
		return "GuideDays"
	case "pseudo":
		return "IncludePseudoTVGuide"
	case "level":
		return "LogLevel"
	case "outdir":
		return "OutDir"
	case "device":
		return "TabloDevice"
	case "user":
		return "UserName"
	case "pass":
		return "UserPass"
	case "ip_address":
		return "IPAddress"
	case "guide":
		return "GuideInterval"
	case "ott":
		return "IncludeOTT"
	case "db":
		return "DBPath"
	case "admin_password":
		return "AdminPassword"
	default:
		return ""
	}
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values, scanner.Err()
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func firstIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if ok && ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

func normalizeLogLevel(level string) string {
	switch strings.ToLower(level) {
	case "info", "error", "warn", "debug":
		return strings.ToLower(level)
	default:
		return "error"
	}
}

func ffmpegLogLevel(level string) string {
	switch level {
	case "debug":
		return "debug"
	case "warn":
		return "warning"
	case "info":
		return "info"
	default:
		return "panic"
	}
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
