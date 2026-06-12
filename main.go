package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	logger, err := NewLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger error: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := NewApp(cfg, logger)
	lineupScheduler := NewScheduler(cfg.OutDir, "schedule_lineup.json", "channel lineup update", cfg.LineupInterval, logger, app.MakeLineup)
	guideScheduler := NewScheduler(cfg.OutDir, "schedule_guide.json", "guide data update", cfg.GuideInterval, logger, app.CacheGuideData)

	if err := app.EnsureCredentials(ctx); err != nil {
		logger.Error("credentials setup failed: %v", err)
		os.Exit(1)
	}
	if cfg.ForceCreds {
		if err := lineupScheduler.RunNow(ctx); err != nil {
			logger.Error("lineup update failed: %v", err)
			os.Exit(1)
		}
		logger.Info("Forced credentials creation complete.")
		return
	}
	if cfg.ForceLineup {
		if err := lineupScheduler.RunNow(ctx); err != nil {
			logger.Error("lineup update failed: %v", err)
			os.Exit(1)
		}
		if cfg.CreateXML {
			if err := guideScheduler.RunNow(ctx); err != nil {
				logger.Error("guide update failed: %v", err)
				os.Exit(1)
			}
		}
		logger.Info("Forced lineup update complete.")
		return
	}
	if !fileExists(app.lineupPath()) {
		if err := lineupScheduler.RunNow(ctx); err != nil {
			logger.Error("initial lineup update failed: %v", err)
			os.Exit(1)
		}
	}
	if err := app.LoadLineup(); err != nil {
		logger.Error("could not read lineup file %s: %v", filepath.Join(cfg.OutDir, "lineup.json"), err)
		os.Exit(1)
	}
	if cfg.CreateXML && !fileExists(app.guidePath()) {
		if err := guideScheduler.RunNow(ctx); err != nil {
			logger.Error("initial guide update failed: %v", err)
			os.Exit(1)
		}
	}
	lineupScheduler.Start(ctx)
	if cfg.CreateXML {
		guideScheduler.Start(ctx)
	}
	if err := app.RunServer(ctx); err != nil {
		logger.Error("server failed: %v", err)
		os.Exit(1)
	}
}
