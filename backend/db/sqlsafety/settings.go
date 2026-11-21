package sqlsafety

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Settings controls risk thresholds and local backup retention.
type Settings struct {
	Enabled         bool  `json:"enabled"`
	HighRiskRows    int64 `json:"highRiskRows"`
	RetentionDays   int   `json:"retentionDays"`
	MaxSnapshotRows int64 `json:"maxSnapshotRows"`
	MaxTotalBytes   int64 `json:"maxTotalBytes"`
}

const settingsFile = "settings.json"

func DefaultSettings() Settings {
	return Settings{Enabled: true, HighRiskRows: 1000, RetentionDays: 7, MaxSnapshotRows: 100000, MaxTotalBytes: 2 << 30}
}

func (s Settings) Validate() error {
	switch {
	case s.HighRiskRows < 1 || s.HighRiskRows > 1_000_000:
		return fmt.Errorf("highRiskRows must be between 1 and 1000000, got %d", s.HighRiskRows)
	case s.RetentionDays < 1 || s.RetentionDays > 30:
		return fmt.Errorf("retentionDays must be between 1 and 30, got %d", s.RetentionDays)
	case s.MaxSnapshotRows < 1 || s.MaxSnapshotRows > 100_000:
		return fmt.Errorf("maxSnapshotRows must be between 1 and 100000, got %d", s.MaxSnapshotRows)
	case s.MaxTotalBytes < 64<<20 || s.MaxTotalBytes > 20<<30:
		return fmt.Errorf("maxTotalBytes must be between 64 MiB and 20 GiB, got %d", s.MaxTotalBytes)
	}
	return nil
}

// LoadSettings reads dir/settings.json. A missing file yields the defaults
// and no error; an unreadable or invalid file yields the defaults and an
// error, so callers can log it and keep going.
func LoadSettings(dir string) (Settings, error) {
	data, err := os.ReadFile(filepath.Join(dir, settingsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return DefaultSettings(), fmt.Errorf("read sql safety settings: %w", err)
	}
	s := DefaultSettings()
	if err := json.Unmarshal(data, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("parse sql safety settings: %w", err)
	}
	if err := s.Validate(); err != nil {
		return DefaultSettings(), fmt.Errorf("invalid sql safety settings: %w", err)
	}
	return s, nil
}

// SaveSettings validates s and writes it to dir/settings.json (0600).
func SaveSettings(dir string, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sql safety settings: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create sql safety directory: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, settingsFile), data); err != nil {
		return fmt.Errorf("write sql safety settings: %w", err)
	}
	return nil
}
