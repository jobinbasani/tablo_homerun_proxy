package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type ScheduleFile struct {
	IntervalSeconds int64     `json:"intervalSeconds"`
	NextCheck       time.Time `json:"nextCheck"`
}

type Scheduler struct {
	path     string
	label    string
	interval time.Duration
	task     func(context.Context) error
	log      *Logger
}

func NewScheduler(outDir, filename, label string, interval time.Duration, logger *Logger, task func(context.Context) error) *Scheduler {
	return &Scheduler{
		path:     filepath.Join(outDir, filename),
		label:    label,
		interval: interval,
		task:     task,
		log:      logger,
	}
}

func (s *Scheduler) RunNow(ctx context.Context) error {
	s.log.Info("Running %s...", s.label)
	if err := s.task(ctx); err != nil {
		return err
	}
	next := time.Now().Add(s.interval)
	if err := s.save(next); err != nil {
		return err
	}
	s.log.Info("%s finished. Next run scheduled for %s.", s.label, next.Format(time.RFC1123))
	return nil
}

func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		for {
			next := s.nextCheck()
			delay := time.Until(next)
			if delay <= 0 {
				if err := s.RunNow(ctx); err != nil {
					s.log.Error("%s failed: %v", s.label, err)
				}
				continue
			}
			s.log.Info("%s scheduled for %s.", s.label, next.Format(time.RFC1123))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				if err := s.RunNow(ctx); err != nil {
					s.log.Error("%s failed: %v", s.label, err)
				}
			}
		}
	}()
}

func (s *Scheduler) nextCheck() time.Time {
	data, err := os.ReadFile(s.path)
	if err != nil {
		next := time.Now()
		_ = s.save(next)
		return next
	}
	var file ScheduleFile
	if err := json.Unmarshal(data, &file); err != nil || file.NextCheck.IsZero() {
		next := time.Now()
		_ = s.save(next)
		return next
	}
	return file.NextCheck
}

func (s *Scheduler) save(next time.Time) error {
	return writeJSONFile(s.path, ScheduleFile{
		IntervalSeconds: int64(s.interval.Seconds()),
		NextCheck:       next,
	})
}
