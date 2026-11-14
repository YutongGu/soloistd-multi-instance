// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveAndLoadConfigSecurely(t *testing.T) {
	root := t.TempDir()
	p := paths{configFile: filepath.Join(root, "config", "soloistd", "config.json")}
	volume := 37
	want := config{DeviceName: "Mopi's PC", APIKey: "very-secret", InitialVolume: &volume}

	if err := saveConfig(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded config = %#v, want %#v", got, want)
	}
	info, err := os.Stat(p.configFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(p.configFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("config directory mode = %o, want 700", got)
	}
}

func TestSetupAPIKeyOnlyCreatesAndUpdatesConfig(t *testing.T) {
	p := testPaths(t)
	a := app{stdin: nil, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	if err := a.setup(context.Background(), []string{"--api-key", "first"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(p)
	if err != nil || cfg.APIKey != "first" || cfg.DeviceName != "" || len(cfg.Instances) != 0 {
		t.Fatalf("key-only config = %#v, %v", cfg, err)
	}
	if _, err := os.Stat(p.soloistBinary()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup downloaded Soloist: %v", err)
	}
	info, err := os.Stat(p.configFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, %v", info, err)
	}
	cfg.Instances = []config{{Name: "kitchen", DeviceName: "Kitchen"}}
	if err := saveConfig(p, cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.setup(context.Background(), []string{"--api-key", "second"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(p)
	if err != nil || cfg.APIKey != "second" || len(cfg.Instances) != 1 || cfg.Instances[0].DeviceName != "Kitchen" {
		t.Fatalf("updated config = %#v, %v", cfg, err)
	}
}

func TestSoloistArgs(t *testing.T) {
	cacheSize := 500
	volume := 42
	cfg := config{
		DeviceName:     "Bedroom",
		APIKey:         "secret",
		CacheSizeMB:    &cacheSize,
		PipewireDevice: "sink-1",
		InitialVolume:  &volume,
		WebSocket:      "127.0.0.1:0",
	}
	p := paths{soloistData: "/data/soloist", soloistCache: "/cache/soloist"}
	want := []string{
		"--device-name", "Bedroom", "--api-key", "secret",
		"--data-dir", "/data/soloist", "--cache-dir", "/cache/soloist",
		"--cache-size", "500", "--pipewire-device", "sink-1",
		"--initial-volume", "42", "--ws", "127.0.0.1:0",
	}
	if got := cfg.soloistArgs(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("soloistArgs() = %#v, want %#v", got, want)
	}
}

func TestValidateConfigRejectsInvalidAudioValues(t *testing.T) {
	tooSmall := 99
	if err := validateConfig(config{DeviceName: "x", APIKey: "y", CacheSizeMB: &tooSmall}); err == nil {
		t.Fatal("expected invalid cache size to fail")
	}
	tooLoud := 101
	if err := validateConfig(config{DeviceName: "x", APIKey: "y", InitialVolume: &tooLoud}); err == nil {
		t.Fatal("expected invalid volume to fail")
	}
}

func TestDefaultDeviceName(t *testing.T) {
	if got := defaultDeviceName("framework13"); got != "soloistd@framework13" {
		t.Fatalf("defaultDeviceName() = %q", got)
	}
	if got := defaultDeviceName("  "); got != "soloistd@host" {
		t.Fatalf("empty defaultDeviceName() = %q", got)
	}
}

func TestPromptReadStopsWhenContextIsCanceled(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := readLineContext(ctx, bufio.NewReader(reader)); !errors.Is(err, context.Canceled) {
		t.Fatalf("readLineContext() error = %v, want context.Canceled", err)
	}
}

func TestNamedInstancesHaveIndependentPathsAndKeys(t *testing.T) {
	p := paths{managerData: "/data/soloistd", soloistData: "/data/soloist", soloistCache: "/cache/soloist"}
	cfg := config{DeviceName: "Original", APIKey: "shared", Instances: []config{
		{Name: "kitchen", DeviceName: "Kitchen", PipewireDevice: "sink-a"},
		{Name: "bedroom", DeviceName: "Bedroom", APIKey: "other", PipewireDevice: "sink-b"},
	}}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	instances := cfg.activeInstances(p)
	if len(instances) != 3 {
		t.Fatalf("got %d instances", len(instances))
	}
	if instances[0].DataDir != "" || instances[1].DataDir != "/data/soloistd/instances/kitchen" || instances[2].DataDir != "/data/soloistd/instances/bedroom" {
		t.Fatalf("data paths: %#v", instances)
	}
	if instances[1].CacheDir != "/cache/soloistd/kitchen" || instances[2].CacheDir != "/cache/soloistd/bedroom" {
		t.Fatalf("cache paths: %#v", instances)
	}
	if instances[1].APIKey != "shared" || instances[2].APIKey != "other" {
		t.Fatalf("API keys were not resolved")
	}
	if _, err := cfg.instance("", p); err == nil {
		t.Fatal("ambiguous instance should fail")
	}
	if got, err := cfg.instance("bedroom", p); err != nil || got.PipewireDevice != "sink-b" {
		t.Fatalf("selected %#v, %v", got, err)
	}
	cfg.Instances = append(cfg.Instances, config{Name: "kitchen", DeviceName: "Duplicate"})
	if err := validateConfig(cfg); err == nil {
		t.Fatal("duplicate instance should fail")
	}
}
