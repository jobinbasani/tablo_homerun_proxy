package tablo

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jobinbasani/tablo_homerun_proxy/internal/storage"
)

type GuideAiring struct {
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	DateTime    string `json:"datetime"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Images      []struct {
		URL string `json:"url"`
	} `json:"images"`
	Duration int `json:"duration"`
	Show     struct {
		Title string `json:"title"`
	} `json:"show"`
	Episode struct {
		EpisodeNumber   *int    `json:"episodeNumber"`
		OriginalAirDate *string `json:"originalAirDate"`
		Rating          *string `json:"rating"`
		Season          struct {
			Kind   string `json:"kind"`
			Number int    `json:"number"`
		} `json:"season"`
	} `json:"episode"`
	MovieAiring struct {
		ReleaseYear int     `json:"releaseYear"`
		FilmRating  *string `json:"filmRating"`
	} `json:"movieAiring"`
}

func (s *Service) CacheGuideData(ctx context.Context) error {
	channels, err := storage.ReadJSONFile[[]Channel](s.lineupPath())
	if err != nil {
		return err
	}
	tempDir := filepath.Join(s.cfg.OutDir, "tempGuide")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return err
	}
	days := guideDays(s.cfg.GuideDays)
	for _, channel := range channels {
		if !s.cfg.IncludeOTT && channel.Kind == "ott" {
			continue
		}
		for _, day := range days {
			fileName := channel.Identifier + "_" + day + ".json"
			path := filepath.Join(tempDir, fileName)
			requestPath := "/api/v2/account/guide/channels/" + channel.Identifier + "/airings/" + day + "/"
			var airings []GuideAiring
			if err := s.lighthouseJSON(ctx, http.MethodGet, requestPath, s.creds.LighthouseTVAuthorization, nil, &airings); err != nil {
				s.log.Warn("guide fetch failed for %s: %v", fileName, err)
				airings = []GuideAiring{}
			}
			if err := storage.WriteJSONFile(path, airings); err != nil {
				return err
			}
		}
	}
	xmlData, err := s.buildXMLGuide(channels, days)
	if err != nil {
		return err
	}
	return storage.WriteFile(s.GuidePath(), []byte(xmlData), 0o644)
}

func (s *Service) buildXMLGuide(channels []Channel, days []string) (string, error) {
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<tv generator-info-name="`)
	writeEscaped(&b, s.cfg.Name)
	b.WriteString(`">` + "\n")
	for _, channel := range channels {
		if !s.cfg.IncludeOTT && channel.Kind == "ott" {
			continue
		}
		id, name, icon := xmlChannel(channel)
		b.WriteString(`  <channel id="`)
		writeEscaped(&b, id)
		b.WriteString(`"><display-name lang="en">`)
		writeEscaped(&b, name)
		b.WriteString(`</display-name>`)
		if icon != "" {
			b.WriteString(`<icon src="`)
			writeEscaped(&b, icon)
			b.WriteString(`"/>`)
		}
		b.WriteString("</channel>\n")
		for _, day := range days {
			path := filepath.Join(s.cfg.OutDir, "tempGuide", channel.Identifier+"_"+day+".json")
			airings, err := storage.ReadJSONFile[[]GuideAiring](path)
			if err != nil {
				return "", err
			}
			for _, airing := range airings {
				stop, ok := airingStop(airing)
				if !ok || stop.Before(time.Now()) {
					continue
				}
				b.WriteString(`  <programme start="`)
				writeEscaped(&b, xmlTime(airing.DateTime))
				b.WriteString(`" stop="`)
				writeEscaped(&b, xmlTime(stop.Format(time.RFC3339)))
				b.WriteString(`" channel="`)
				writeEscaped(&b, id)
				b.WriteString(`">`)
				title := airing.Title
				if airing.Kind == "episode" && airing.Show.Title != "" && airing.Episode.EpisodeNumber != nil {
					title = airing.Show.Title
				}
				b.WriteString(`<title lang="en">`)
				writeEscaped(&b, cleanText(title))
				b.WriteString(`</title>`)
				if airing.Kind == "episode" && airing.Title != "" && airing.Show.Title != "" && airing.Episode.EpisodeNumber != nil {
					b.WriteString(`<previously-shown/><sub-title lang="en">`)
					writeEscaped(&b, cleanText(airing.Title))
					b.WriteString(`</sub-title>`)
				}
				if airing.Description != "" {
					b.WriteString(`<desc lang="en">`)
					writeEscaped(&b, cleanText(airing.Description))
					b.WriteString(`</desc>`)
				}
				if len(airing.Images) > 0 && airing.Images[0].URL != "" {
					b.WriteString(`<icon src="`)
					writeEscaped(&b, airing.Images[0].URL)
					b.WriteString(`"/>`)
				}
				b.WriteString("</programme>\n")
			}
		}
	}
	if s.cfg.IncludePseudoTVGuide {
		pseudoPath := filepath.Join(s.cfg.OutDir, ".pseudotv", "xmltv.xml")
		if data, err := os.ReadFile(pseudoPath); err == nil {
			lines := strings.Split(string(data), "\n")
			if len(lines) > 3 {
				b.WriteString(strings.Join(lines[2:len(lines)-1], "\n"))
				b.WriteByte('\n')
			}
		}
	}
	b.WriteString("</tv>\n")
	return b.String(), nil
}

func xmlChannel(channel Channel) (string, string, string) {
	if channel.Kind == "ott" {
		return fmt.Sprintf("%d%d1", channel.OTT.Major, channel.OTT.Minor), channel.OTT.Network, bestLogo(channel.Logos)
	}
	return fmt.Sprintf("%d%d1", channel.OTA.Major, channel.OTA.Minor), channel.OTA.Network, bestLogo(channel.Logos)
}

func guideDays(count int) []string {
	days := make([]string, 0, count)
	now := time.Now()
	for i := 0; i < count; i++ {
		days = append(days, now.AddDate(0, 0, i).Format("2006-01-02"))
	}
	return days
}

func airingStop(airing GuideAiring) (time.Time, bool) {
	start, err := time.Parse(time.RFC3339, airing.DateTime)
	if err != nil {
		return time.Time{}, false
	}
	return start.Add(time.Duration(airing.Duration) * time.Second), true
}

func xmlTime(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Now().Format("20060102150405 -0700")
	}
	return t.Format("20060102150405 -0700")
}

func writeEscaped(b *strings.Builder, value string) {
	_ = xml.EscapeText(b, []byte(value))
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func rawJSON(data any) string {
	value, _ := json.Marshal(data)
	return string(value)
}
