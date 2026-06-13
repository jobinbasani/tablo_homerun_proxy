package logging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/config"
)

type Logger struct {
	level int
	file  *os.File
	mu    sync.Mutex
}

func New(cfg config.Config) (*Logger, error) {
	logger := &Logger{level: logLevelNumber(cfg.LogLevel)}
	if cfg.SaveLog {
		logDir := filepath.Join(cfg.OutDir, "logs")
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return nil, err
		}
		path := filepath.Join(logDir, time.Now().Format("2006.01.02-03.04.05PM")+"-"+cfg.LogLevel+".log")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		logger.file = file
	}
	return logger, nil
}

func (l *Logger) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *Logger) Info(format string, args ...any) {
	l.write(0, "info", format, args...)
}

func (l *Logger) Error(format string, args ...any) {
	l.write(1, "error", format, args...)
}

func (l *Logger) Warn(format string, args ...any) {
	l.write(2, "warn", format, args...)
}

func (l *Logger) Debug(format string, args ...any) {
	l.write(3, "debug", format, args...)
}

func (l *Logger) write(required int, label string, format string, args ...any) {
	if l.level < required {
		return
	}
	message := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s", label, message)
	l.mu.Lock()
	defer l.mu.Unlock()
	log.Println(line)
	if l.file != nil {
		_, _ = l.file.WriteString(time.Now().Format(time.RFC3339) + " " + line + "\n")
	}
}

func logLevelNumber(level string) int {
	switch level {
	case "debug":
		return 3
	case "warn":
		return 2
	case "error":
		return 1
	default:
		return 0
	}
}
