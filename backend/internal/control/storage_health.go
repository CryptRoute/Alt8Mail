package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type storageDiskHealth struct {
	State        string `json:"state"`
	Message      string `json:"message,omitempty"`
	Passed       *bool  `json:"passed,omitempty"`
	TemperatureC *int   `json:"temperatureC,omitempty"`
	Reallocated  *int64 `json:"reallocatedSectors,omitempty"`
	Pending      *int64 `json:"pendingSectors,omitempty"`
	MediaErrors  *int64 `json:"mediaErrors,omitempty"`
	PowerOnHours *int64 `json:"powerOnHours,omitempty"`
	CheckedAt    string `json:"checkedAt"`
}

type storageDiskHealthCache struct {
	health storageDiskHealth
	at     time.Time
}

var storageHealthCommand = runCommand

func (s *Server) storageDiskHealth(ctx context.Context, disk storageDisk) *storageDiskHealth {
	if storageHostGOOS != "linux" || disk.Type != "disk" || !strings.HasPrefix(disk.Path, "/dev/") {
		return nil
	}
	if _, err := storageLookPath("smartctl"); err != nil {
		return nil
	}
	key := disk.Serial
	if key == "" {
		key = disk.WWN
	}
	if key == "" {
		key = disk.Path
	}
	s.storageHealthMu.Lock()
	if entry, ok := s.storageHealthCache[key]; ok && time.Since(entry.at) < 5*time.Minute {
		health := entry.health
		s.storageHealthMu.Unlock()
		return &health
	}
	s.storageHealthMu.Unlock()
	health := readStorageDiskHealth(ctx, disk.Path)
	s.storageHealthMu.Lock()
	if s.storageHealthCache == nil {
		s.storageHealthCache = map[string]storageDiskHealthCache{}
	}
	s.storageHealthCache[key] = storageDiskHealthCache{health: health, at: time.Now()}
	s.storageHealthMu.Unlock()
	return &health
}

func readStorageDiskHealth(ctx context.Context, path string) storageDiskHealth {
	health := storageDiskHealth{State: "unknown", CheckedAt: nowRFC3339()}
	result, err := storageHealthCommand(ctx, 6*time.Second, "smartctl", "-H", "-A", "-j", path)
	if strings.TrimSpace(result.Stdout) == "" {
		health.Message = truncateText(strings.TrimSpace(result.Stderr+" "+errString(err)), 240)
		if health.Message == "" {
			health.Message = "SMART data is unavailable for this device."
		}
		return health
	}
	var payload struct {
		SmartStatus *struct {
			Passed *bool `json:"passed"`
		} `json:"smart_status"`
		Temperature *struct {
			Current *int `json:"current"`
		} `json:"temperature"`
		PowerOnTime *struct {
			Hours *int64 `json:"hours"`
		} `json:"power_on_time"`
		ATA *struct {
			Table []struct {
				ID  int `json:"id"`
				Raw struct {
					Value *int64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		NVMe *struct {
			MediaErrors *int64 `json:"media_errors"`
		} `json:"nvme_smart_health_information_log"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		health.Message = "SMART output could not be parsed."
		return health
	}
	if payload.SmartStatus != nil {
		health.Passed = payload.SmartStatus.Passed
	}
	if payload.Temperature != nil {
		health.TemperatureC = payload.Temperature.Current
	}
	if payload.PowerOnTime != nil {
		health.PowerOnHours = payload.PowerOnTime.Hours
	}
	if payload.ATA != nil {
		for _, attribute := range payload.ATA.Table {
			value := int64(0)
			if attribute.Raw.Value != nil {
				value = *attribute.Raw.Value
			}
			switch attribute.ID {
			case 5:
				health.Reallocated = &value
			case 197:
				health.Pending = &value
			case 198:
				health.MediaErrors = &value
			}
		}
	}
	if payload.NVMe != nil && payload.NVMe.MediaErrors != nil {
		health.MediaErrors = payload.NVMe.MediaErrors
	}
	warnings := []string{}
	if health.Passed != nil && !*health.Passed {
		health.State = "critical"
		warnings = append(warnings, "SMART self-assessment failed")
	}
	if health.Pending != nil && *health.Pending > 0 {
		if health.State != "critical" {
			health.State = "warning"
		}
		warnings = append(warnings, fmt.Sprintf("%d pending sectors", *health.Pending))
	}
	if health.Reallocated != nil && *health.Reallocated > 0 {
		if health.State == "unknown" || health.State == "ok" {
			health.State = "warning"
		}
		warnings = append(warnings, fmt.Sprintf("%d reallocated sectors", *health.Reallocated))
	}
	if health.MediaErrors != nil && *health.MediaErrors > 0 {
		if health.State != "critical" {
			health.State = "warning"
		}
		warnings = append(warnings, fmt.Sprintf("%d media errors", *health.MediaErrors))
	}
	if health.TemperatureC != nil {
		switch {
		case *health.TemperatureC >= 60:
			health.State = "critical"
			warnings = append(warnings, fmt.Sprintf("%d°C", *health.TemperatureC))
		case *health.TemperatureC >= 50:
			if health.State == "unknown" || health.State == "ok" {
				health.State = "warning"
			}
			warnings = append(warnings, fmt.Sprintf("%d°C", *health.TemperatureC))
		}
	}
	if health.State == "unknown" && health.Passed != nil && *health.Passed {
		health.State = "ok"
	}
	health.Message = strings.Join(warnings, " · ")
	if health.Message == "" && health.State == "ok" {
		health.Message = "SMART self-assessment passed"
	}
	if health.Message == "" {
		health.Message = "SMART health could not be determined"
	}
	return health
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
