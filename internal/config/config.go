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
	SaveLog              bool
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
}

type EnvConfig struct {
	Name                 string `envconfig:"NAME" default:"Tablo 4th Gen Proxy"`
	DeviceID             string `envconfig:"DEVICE_ID" default:"12345679"`
	Port                 string `envconfig:"PORT" default:"8181"`
	LineupUpdateInterval int    `envconfig:"LINEUP_UPDATE_INTERVAL" default:"30"`
	CreateXML            bool   `envconfig:"CREATE_XML" default:"false"`
	GuideDays            int    `envconfig:"GUIDE_DAYS" default:"2"`
	IncludePseudoTVGuide bool   `envconfig:"INCLUDE_PSEUDOTV_GUIDE" default:"false"`
	LogLevel             string `envconfig:"LOG_LEVEL" default:"error"`
	SaveLog              bool   `envconfig:"SAVE_LOG" default:"false"`
	OutDir               string `envconfig:"OUT_DIR" default:""`
	TabloDevice          string `envconfig:"TABLO_DEVICE" default:""`
	UserName             string `envconfig:"USER_NAME" default:""`
	UserPass             string `envconfig:"USER_PASS" default:""`
	IPAddress            string `envconfig:"IP_ADDRESS" default:""`
	GuideUpdateInterval  int    `envconfig:"GUIDE_UPDATE_INTERVAL" default:"24"`
	IncludeOTT           bool   `envconfig:"INCLUDE_OTT" default:"true"`
}

func Load() (Config, error) {
	baseDir, err := os.Getwd()
	if err != nil {
		return Config{}, err
	}
	envPath := filepath.Join(baseDir, ".env")
	if err := ensureEnvFile(envPath); err != nil {
		return Config{}, err
	}
	env, err := readEnvFile(envPath)
	if err != nil {
		return Config{}, err
	}
	for key, value := range env {
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}

	envCfg := EnvConfig{}
	if err := envconfig.Process("", &envCfg); err != nil {
		return Config{}, err
	}

	cfg := Config{}
	flag.BoolVar(&cfg.ForceCreds, "creds", false, "force creation of a new credentials file")
	flag.BoolVar(&cfg.ForceCreds, "c", false, "force creation of a new credentials file")
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
	saveLog := flag.Bool("log", envCfg.SaveLog, "write logs to disk")
	outDir := flag.String("outdir", envCfg.OutDir, "output directory")
	tabloDevice := flag.String("device", envCfg.TabloDevice, "Tablo server ID")
	user := flag.String("user", envCfg.UserName, "Tablo username")
	pass := flag.String("pass", envCfg.UserPass, "Tablo password")
	ip := flag.String("ip_address", envCfg.IPAddress, "static IP address for advertised server URL")
	guideHours := flag.Int("guide", envCfg.GuideUpdateInterval, "guide update interval in hours")
	ott := flag.Bool("ott", envCfg.IncludeOTT, "include OTT channels")
	flag.Parse()

	cfg.Name = *name
	cfg.DeviceID = *deviceID
	cfg.Port = *port
	cfg.LineupInterval = time.Duration(*lineupDays) * 24 * time.Hour
	cfg.CreateXML = *createXML
	cfg.GuideDays = clamp(*guideDays, 1, 7)
	cfg.IncludePseudoTVGuide = *pseudo
	cfg.LogLevel = normalizeLogLevel(*logLevel)
	cfg.SaveLog = *saveLog
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
	if *ip == "" {
		cfg.IPAddress = firstIPv4()
	} else {
		cfg.IPAddress = *ip
	}
	cfg.ServerURL = fmt.Sprintf("http://%s:%s", cfg.IPAddress, cfg.Port)
	cfg.FFmpegLogLevel = ffmpegLogLevel(cfg.LogLevel)
	return cfg, nil
}

func ensureEnvFile(path string) error {
	defaults := []string{
		`NAME="Tablo 4th Gen Proxy"`,
		`DEVICE_ID="12345679"`,
		`PORT="8181"`,
		`LINEUP_UPDATE_INTERVAL="30"`,
		`CREATE_XML="false"`,
		`GUIDE_DAYS="2"`,
		`INCLUDE_PSEUDOTV_GUIDE="false"`,
		`LOG_LEVEL="error"`,
		`SAVE_LOG="false"`,
		`OUT_DIR=""`,
		`TABLO_DEVICE=""`,
		`USER_NAME=""`,
		`USER_PASS=""`,
		`IP_ADDRESS=""`,
		`GUIDE_UPDATE_INTERVAL="24"`,
		`INCLUDE_OTT="true"`,
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.WriteFile(path, []byte(strings.Join(defaults, "\n")+"\n"), 0o600)
	}
	return nil
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
