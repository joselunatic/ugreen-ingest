// Package config loads application settings from a YAML file with
// environment variable overrides, per RUNBOOK_MOY_MEDIA_INGEST.md section 15.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode            string       `yaml:"mode"`
	Listen          string       `yaml:"listen"`
	Database        string       `yaml:"database"`
	DestinationRoot string       `yaml:"destination_root"`
	MockRoot        string       `yaml:"mock_root"`
	Project         Project      `yaml:"project"`
	Cameras         []string     `yaml:"cameras"`
	Verification    Verification `yaml:"verification"`
	Formatting      Formatting   `yaml:"formatting"`
}

type Project struct {
	DefaultName string `yaml:"default_name"`
}

type Verification struct {
	Algorithm string `yaml:"algorithm"`
}

type Formatting struct {
	Enabled bool `yaml:"enabled"`
}

const (
	ModeMock = "mock"
	ModeReal = "real"
)

// ExtraCamera is always offered in the UI in addition to config.Cameras,
// matching the "OTRA/OTHER" button from the runbook UI mockups.
const ExtraCamera = "OTHER"

func Default() *Config {
	return &Config{
		Mode:            ModeMock,
		Listen:          "0.0.0.0:8080",
		Database:        "./data/state/ingest.db",
		DestinationRoot: "./dev/ingest",
		MockRoot:        "./dev/cards",
		Project:         Project{DefaultName: "TEST_EVENT"},
		Cameras:         []string{"CAM01", "CAM02", "CAM03", "DRONE", "AUDIO"},
		Verification:    Verification{Algorithm: "sha256"},
		Formatting:      Formatting{Enabled: false},
	}
}

// Load reads the YAML file at path (if it exists) on top of the defaults,
// then applies environment variable overrides. A missing file is not an
// error: defaults + env vars are enough to run.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parse config %s: %w", path, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	applyEnvOverrides(cfg)

	if cfg.Mode != ModeMock && cfg.Mode != ModeReal {
		return nil, fmt.Errorf("invalid mode %q: must be %q or %q", cfg.Mode, ModeMock, ModeReal)
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("INGEST_MODE"); v != "" {
		cfg.Mode = v
	}
	if v := os.Getenv("INGEST_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("INGEST_DATABASE"); v != "" {
		cfg.Database = v
	}
	if v := os.Getenv("INGEST_DESTINATION_ROOT"); v != "" {
		cfg.DestinationRoot = v
	}
	if v := os.Getenv("INGEST_MOCK_ROOT"); v != "" {
		cfg.MockRoot = v
	}
	if v := os.Getenv("INGEST_PROJECT_NAME"); v != "" {
		cfg.Project.DefaultName = v
	}
	if v := os.Getenv("INGEST_VERIFY_ALGORITHM"); v != "" {
		cfg.Verification.Algorithm = v
	}
}
