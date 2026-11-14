// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDynamicInstanceLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Soloist is Linux-only")
	}
	p := testPaths(t)
	if err := os.MkdirAll(filepath.Dir(p.soloistBinary()), 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(t.TempDir(), "events")
	t.Setenv("SOLOIST_TEST_EVENTS", logFile)
	script := `#!/bin/sh
name= key=
while [ "$#" -gt 0 ]; do
 case "$1" in --device-name) name=$2; shift 2;; --api-key) key=$2; shift 2;; *) shift;; esac
done
printf 'start:%s:%s\n' "$name" "$key" >> "$SOLOIST_TEST_EVENTS"
trap 'printf "stop:%s\n" "$name" >> "$SOLOIST_TEST_EVENTS"; exit 0' TERM
while :; do sleep 0.05; done
`
	if err := os.WriteFile(p.soloistBinary(), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldURL := downloadBaseURL
	downloadBaseURL = "http://127.0.0.1:1/"
	defer func() { downloadBaseURL = oldURL }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	a := app{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr}
	done := make(chan error, 1)
	go func() { done <- a.runDaemon(ctx, nil) }()
	waitFor := func(want string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			var got string
			if data, err := os.ReadFile(logFile); err == nil {
				got = string(data)
			}
			if want == "socket" {
				if _, err := os.Stat(p.controlSocket()); err == nil {
					return
				}
			} else if strings.Contains(got, want) {
				return
			}
			select {
			case err := <-done:
				t.Fatalf("daemon exited: %v; stderr: %s", err, stderr.String())
			case <-deadline:
				t.Fatalf("timed out waiting for %q; events: %s; stderr: %s", want, got, stderr.String())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	waitFor("socket")
	socketInfo, err := os.Stat(p.controlSocket())
	if err != nil || socketInfo.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v", socketInfo, err)
	}
	add := func(name, device, key string) {
		t.Helper()
		cli := app{stdin: strings.NewReader(key + "\n"), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		args := []string{"add", "--name", name, "--device-name", device}
		if err := cli.instanceCommand(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	add("kitchen", "Kitchen", "secret")
	waitFor("start:Kitchen")
	add("bedroom", "Bedroom", "")
	waitFor("start:Bedroom")
	setupClient := app{stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	if err := setupClient.setup(ctx, []string{"--api-key", "rotated"}); err != nil {
		t.Fatal(err)
	}
	waitFor("start:Kitchen:rotated")
	waitFor("start:Bedroom:rotated")
	eventsBeforeRemoval, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	bedroomStopsBeforeRemoval := strings.Count(string(eventsBeforeRemoval), "stop:Bedroom")
	list, err := sendControl(ctx, p, instanceCommand{Action: "list"})
	if err != nil || len(list.Instances) != 2 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if _, err := sendControl(ctx, p, instanceCommand{Action: "add", Instance: config{Name: "kitchen", DeviceName: "Duplicate"}}); err == nil {
		t.Fatal("duplicate add succeeded")
	}
	if _, err := sendControl(ctx, p, instanceCommand{Action: "remove", Name: "kitchen"}); err != nil {
		t.Fatal(err)
	}
	waitFor("stop:Kitchen")
	events, _ := os.ReadFile(logFile)
	if strings.Count(string(events), "stop:Bedroom") != bedroomStopsBeforeRemoval {
		t.Fatal("removing Kitchen stopped Bedroom")
	}
	persisted, err := loadConfig(p)
	if err != nil || len(persisted.Instances) != 1 || persisted.Instances[0].Name != "bedroom" || persisted.APIKey != "rotated" {
		t.Fatalf("persisted config = %#v, %v", persisted, err)
	}
	if _, err := sendControl(ctx, p, instanceCommand{Action: "remove", Name: "bedroom"}); err != nil {
		t.Fatal(err)
	}
	list, err = sendControl(ctx, p, instanceCommand{Action: "list"})
	if err != nil || len(list.Instances) != 0 {
		t.Fatalf("empty list = %#v, %v", list, err)
	}
	add("kitchen", "Kitchen", "")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.controlSocket()); !os.IsNotExist(err) {
		t.Fatalf("socket still exists: %v", err)
	}
	restartCtx, restartCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer restartCancel()
	var restartOutput, restartErrors bytes.Buffer
	restarted := app{stdin: strings.NewReader(""), stdout: &restartOutput, stderr: &restartErrors}
	restartedDone := make(chan error, 1)
	go func() { restartedDone <- restarted.runDaemon(restartCtx, nil) }()
	deadline := time.After(2 * time.Second)
	for {
		if _, err := os.Stat(p.controlSocket()); err == nil {
			break
		}
		select {
		case err := <-restartedDone:
			t.Fatalf("restart failed: %v; %s", err, restartErrors.String())
		case <-deadline:
			t.Fatalf("restarted manager did not open socket: %s", restartErrors.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	list, err = sendControl(restartCtx, p, instanceCommand{Action: "list"})
	if err != nil || len(list.Instances) != 1 || list.Instances[0].Name != "kitchen" {
		t.Fatalf("restored list = %#v, %v", list, err)
	}
	restartCancel()
	if err := <-restartedDone; err != nil {
		t.Fatal(err)
	}
}
