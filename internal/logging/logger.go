package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
)

type Logger struct {
	minSeverity int
	mu          sync.Mutex
}

type entry struct {
	Time    string `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"msg"`
	Service string `json:"service"`
}

func New(cfg config.Config) (*Logger, error) {
	return &Logger{minSeverity: severity(cfg.LogLevel)}, nil
}

func (l *Logger) Close() error {
	return nil
}

func (l *Logger) Info(format string, args ...any) {
	l.write(0, "info", os.Stdout, format, args...)
}

func (l *Logger) Error(format string, args ...any) {
	l.write(3, "error", os.Stderr, format, args...)
}

func (l *Logger) Warn(format string, args ...any) {
	l.write(2, "warn", os.Stdout, format, args...)
}

func (l *Logger) Debug(format string, args ...any) {
	l.write(-1, "debug", os.Stdout, format, args...)
}

func (l *Logger) Always(format string, args ...any) {
	l.writeBypass("info", os.Stdout, format, args...)
}

func (l *Logger) write(messageSeverity int, label string, out io.Writer, format string, args ...any) {
	if messageSeverity < l.minSeverity {
		return
	}
	l.writeBypass(label, out, format, args...)
}

func (l *Logger) writeBypass(label string, out io.Writer, format string, args ...any) {
	record := entry{
		Time:    time.Now().UTC().Format(time.RFC3339Nano),
		Level:   label,
		Message: fmt.Sprintf(format, args...),
		Service: "tablo-homerun-proxy",
	}
	data, err := json.Marshal(record)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"ts":"%s","level":"error","msg":"failed to encode log record","service":"tablo-homerun-proxy"}`, record.Time))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = out.Write(append(data, '\n'))
}

func severity(level string) int {
	switch level {
	case "debug":
		return -1
	case "warn":
		return 2
	case "error":
		return 3
	default:
		return 0
	}
}
