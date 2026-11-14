// SPDX-License-Identifier: MPL-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

type config struct {
	Name           string   `json:"name,omitempty"`
	Instances      []config `json:"instances,omitempty"`
	DeviceName     string   `json:"device_name,omitempty"`
	APIKey         string   `json:"api_key,omitempty"`
	DataDir        string   `json:"data_dir,omitempty"`
	CacheDir       string   `json:"cache_dir,omitempty"`
	CacheSizeMB    *int     `json:"cache_size_mb,omitempty"`
	PipewireDevice string   `json:"pipewire_device,omitempty"`
	InitialVolume  *int     `json:"initial_volume,omitempty"`
	WebSocket      string   `json:"websocket,omitempty"`
}

func loadConfig(p paths) (config, error) {
	data, err := os.ReadFile(p.configFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config{}, fmt.Errorf("not configured; run 'soloistd setup' first")
		}
		return config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", p.configFile, err)
	}
	if err := validateConfig(cfg); err != nil {
		return config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func loadConfigOptional(p paths) (config, error) {
	cfg, err := loadConfig(p)
	if err == nil {
		return cfg, nil
	}
	if _, statErr := os.Stat(p.configFile); errors.Is(statErr, os.ErrNotExist) {
		return config{}, nil
	}
	return config{}, err
}

var instanceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func validateConfig(cfg config) error {
	if cfg.DeviceName == "" && len(cfg.Instances) == 0 {
		return nil
	}
	if len(cfg.Instances) > 0 {
		seen := map[string]bool{}
		if cfg.DeviceName != "" {
			seen["default"] = true
		}
		for _, inst := range cfg.Instances {
			if !instanceNamePattern.MatchString(inst.Name) || inst.Name == "default" {
				return fmt.Errorf("invalid instance name %q (use letters, digits, _ or -; default is reserved)", inst.Name)
			}
			if seen[inst.Name] {
				return fmt.Errorf("duplicate instance name %q", inst.Name)
			}
			seen[inst.Name] = true
			if len(inst.Instances) != 0 {
				return errors.New("nested instances are not supported")
			}
			if inst.APIKey == "" {
				inst.APIKey = cfg.APIKey
			}
			if err := validateInstance(inst); err != nil {
				return fmt.Errorf("instance %q: %w", inst.Name, err)
			}
		}
	}
	if cfg.DeviceName == "" {
		return nil
	}
	return validateInstance(cfg)
}

func validateInstance(cfg config) error {
	if cfg.DeviceName == "" {
		return errors.New("device_name is required")
	}
	if cfg.APIKey == "" {
		return errors.New("api_key is required")
	}
	if cfg.CacheSizeMB != nil && *cfg.CacheSizeMB != 0 && *cfg.CacheSizeMB < 100 {
		return errors.New("cache_size_mb must be 0 or at least 100")
	}
	if cfg.InitialVolume != nil && (*cfg.InitialVolume < 0 || *cfg.InitialVolume > 100) {
		return errors.New("initial_volume must be between 0 and 100")
	}
	return nil
}

func (cfg config) activeInstances(p paths) []config {
	var result []config
	if cfg.DeviceName != "" {
		result = append(result, cfg)
	}
	for _, inst := range cfg.Instances {
		if inst.APIKey == "" {
			inst.APIKey = cfg.APIKey
		}
		if inst.DataDir == "" {
			inst.DataDir = filepath.Join(p.managerData, "instances", inst.Name)
		}
		if inst.CacheDir == "" {
			inst.CacheDir = filepath.Join(filepath.Dir(p.soloistCache), "soloistd", inst.Name)
		}
		result = append(result, inst)
	}
	return result
}

func (cfg config) instance(name string, p paths) (config, error) {
	instances := cfg.activeInstances(p)
	if name == "" {
		if len(instances) == 0 {
			return config{}, errors.New("no instances configured")
		}
		if len(instances) == 1 {
			return instances[0], nil
		}
		return config{}, errors.New("multiple instances configured; specify --instance NAME")
	}
	for _, inst := range instances {
		id := inst.Name
		if id == "" {
			id = "default"
		}
		if id == name {
			return inst, nil
		}
	}
	return config{}, fmt.Errorf("unknown instance %q", name)
}

func saveConfig(p paths, cfg config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.configFile), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(p.configFile), 0o700); err != nil {
		return fmt.Errorf("secure config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p.configFile), ".config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, p.configFile); err != nil {
		return err
	}
	return os.Chmod(p.configFile, 0o600)
}

func (cfg config) soloistArgs(p paths) []string {
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = p.soloistData
	}
	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = p.soloistCache
	}
	args := []string{
		"--device-name", cfg.DeviceName,
		"--api-key", cfg.APIKey,
		"--data-dir", dataDir,
		"--cache-dir", cacheDir,
	}
	if cfg.CacheSizeMB != nil {
		args = append(args, "--cache-size", fmt.Sprint(*cfg.CacheSizeMB))
	}
	if cfg.PipewireDevice != "" {
		args = append(args, "--pipewire-device", cfg.PipewireDevice)
	}
	if cfg.InitialVolume != nil {
		args = append(args, "--initial-volume", fmt.Sprint(*cfg.InitialVolume))
	}
	if cfg.WebSocket != "" {
		args = append(args, "--ws", cfg.WebSocket)
	}
	return args
}
