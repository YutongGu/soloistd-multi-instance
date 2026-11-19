// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSoloistOutputUsesSeparatePrivateLogFiles(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "soloist")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'stdout:%s\\n' \"$1\"\nprintf 'stderr:%s\\n' \"$1\" >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := app{stdout: &stdout, stderr: &stderr}
	logs := filepath.Join(root, "logs")
	for _, name := range []string{"kitchen", "bedroom", "kitchen"} {
		code, err, updated := a.runSoloistOnce(context.Background(), binary, []string{name}, filepath.Join(logs, name+".log"), nil, nil)
		if code != 0 || err != nil || updated {
			t.Fatalf("run %s: code=%d err=%v updated=%v", name, code, err, updated)
		}
	}
	for _, name := range []string{"kitchen", "bedroom"} {
		path := filepath.Join(logs, name+".log")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		wantCount := 1
		if name == "kitchen" {
			wantCount = 2
		}
		if strings.Count(string(data), "stdout:"+name) != wantCount || strings.Count(string(data), "stderr:"+name) != wantCount {
			t.Fatalf("wrong output in %s: %q", path, data)
		}
		if strings.Contains(string(data), "stdout:"+map[string]string{"kitchen": "bedroom", "bedroom": "kitchen"}[name]) {
			t.Fatalf("mixed output in %s: %q", path, data)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("log mode for %s: %v, %v", path, info, err)
		}
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("Soloist output reached manager streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestLoginFailureWriterDetectsSplitMessage(t *testing.T) {
	var output bytes.Buffer
	var failed atomic.Bool
	writer := &loginFailureWriter{output: &output, failed: &failed}
	for _, piece := range []string{"unrelated\nlogin failed: make sure --api-", "key is valid\n"} {
		if _, err := writer.Write([]byte(piece)); err != nil {
			t.Fatal(err)
		}
	}
	if !failed.Load() || output.String() != "unrelated\n"+invalidAPIKeyMessage+"\n" {
		t.Fatalf("failed=%v output=%q", failed.Load(), output.String())
	}
}

func TestLogPathsMustBeAbsoluteAndDistinct(t *testing.T) {
	p := paths{configFile: "/config/soloistd/config.json", managerData: "/data/soloistd", managerLogs: "/state/soloistd/logs", soloistData: "/data/soloist", soloistCache: "/cache/soloist"}
	if err := validateConfig(config{DeviceName: "Kitchen", APIKey: "key", LogPath: "relative.log"}); err == nil {
		t.Fatal("relative log path accepted")
	}
	cfg := config{APIKey: "key", Instances: []config{
		{Name: "kitchen", DeviceName: "Kitchen", LogPath: "/tmp/shared.log"},
		{Name: "bedroom", DeviceName: "Bedroom", LogPath: "/tmp/shared.log"},
	}}
	if err := validateInstancePaths(cfg.activeInstances(p), p); err == nil {
		t.Fatal("shared log path accepted")
	}
	if got := (config{Name: "kitchen"}).soloistLogPath(p); got != "/state/soloistd/logs/kitchen.log" {
		t.Fatalf("default log path = %q", got)
	}
	if got := (config{}).soloistLogPath(p); got != "/state/soloistd/logs/default.log" {
		t.Fatalf("unnamed log path = %q", got)
	}
}

func TestInstanceLogRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openInstanceLog(link); err == nil {
		t.Fatal("symlink log path accepted")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "untouched" {
		t.Fatalf("target changed: %q, %v", got, err)
	}
}
