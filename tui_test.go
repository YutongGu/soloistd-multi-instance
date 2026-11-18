// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTUIAddsAndViewsInstancesOnDefaultOutput(t *testing.T) {
	p := testPaths(t)
	t.Setenv("SOLOIST_API_KEY", "")
	if err := saveConfig(p, config{APIKey: "shared"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan controlRequest)
	closeControl, err := listenForControl(ctx, p, requests)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	added := make(chan config, 1)
	go func() {
		var instances []instanceInfo
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-requests:
				switch req.command.Action {
				case "list":
					req.reply <- instanceResponse{Instances: instances}
				case "add":
					inst := req.command.Instance
					added <- inst
					instances = append(instances, instanceInfo{Name: inst.Name, DeviceName: inst.DeviceName, PipewireDevice: inst.PipewireDevice, LogPath: inst.LogPath})
					req.reply <- instanceResponse{}
				case "remove":
					instances = nil
					req.reply <- instanceResponse{}
				}
			}
		}
	}()
	var output bytes.Buffer
	logPath := p.managerLogs + "/custom-kitchen.log"
	a := app{stdin: strings.NewReader("a\nkitchen\nKitchen\n" + logPath + "\nv\nr\nkitchen\nq\n"), stdout: &output, stderr: &bytes.Buffer{}}
	if err := a.tui(ctx, nil); err != nil {
		t.Fatal(err)
	}
	inst := <-added
	if inst.Name != "kitchen" || inst.DeviceName != "Kitchen" || inst.PipewireDevice != "" || inst.LogPath != logPath || inst.APIKey != "" {
		t.Fatalf("added instance = %#v", inst)
	}
	if !strings.Contains(output.String(), "kitchen — Kitchen (default PipeWire output)") || !strings.Contains(output.String(), "log: "+logPath) || !strings.Contains(output.String(), "  (none)") {
		t.Fatalf("unexpected TUI output:\n%s", output.String())
	}
	if strings.Contains(output.String(), "Soloist API key:") {
		t.Fatal("TUI prompted despite saved shared key")
	}
}

func TestTUIPromptsForKeyWhenNoneIsSaved(t *testing.T) {
	p := testPaths(t)
	t.Setenv("SOLOIST_API_KEY", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan controlRequest)
	closeControl, err := listenForControl(ctx, p, requests)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	added := make(chan config, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-requests:
				if req.command.Action == "add" {
					added <- req.command.Instance
				}
				req.reply <- instanceResponse{}
			}
		}
	}()
	var output bytes.Buffer
	a := app{stdin: strings.NewReader("a\nbedroom\n\n\nsecret\nq\n"), stdout: &output, stderr: &bytes.Buffer{}}
	if err := a.tui(ctx, nil); err != nil {
		t.Fatal(err)
	}
	inst := <-added
	if inst.Name != "bedroom" || inst.DeviceName != "bedroom" || inst.APIKey != "secret" || inst.PipewireDevice != "" {
		t.Fatalf("added instance = %#v", inst)
	}
	if !strings.Contains(output.String(), "Soloist API key:") || strings.Contains(output.String(), "secret") {
		t.Fatalf("unexpected key prompt output:\n%s", output.String())
	}
}

func TestTUIAPIKeyFlagAppliesToEveryNewInstance(t *testing.T) {
	p := testPaths(t)
	t.Setenv("SOLOIST_API_KEY", "from-env")
	if err := saveConfig(p, config{APIKey: "saved-key"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := make(chan controlRequest)
	closeControl, err := listenForControl(ctx, p, requests)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	added := make(chan instanceCommand, 2)
	go func() {
		var instances []instanceInfo
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-requests:
				if req.command.Action == "add-temporary" {
					added <- req.command
					inst := req.command.Instance
					instances = append(instances, instanceInfo{Name: inst.Name, DeviceName: inst.DeviceName, NoPersist: true})
				}
				if req.command.Action == "list" {
					req.reply <- instanceResponse{Instances: instances}
				} else {
					req.reply <- instanceResponse{}
				}
			}
		}
	}()
	var output bytes.Buffer
	a := app{stdin: strings.NewReader("a\nkitchen\nKitchen\n\na\nbedroom\nBedroom\n\nq\n"), stdout: &output, stderr: &bytes.Buffer{}}
	if err := a.tui(ctx, []string{"--api-key", "from-flag", "--no-persist"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		command := <-added
		if command.Instance.APIKey != "from-flag" || command.Instance.PipewireDevice != "" || command.Action != "add-temporary" {
			t.Fatalf("added instance %d = %#v", i, command)
		}
	}
	if strings.Contains(output.String(), "Soloist API key:") || strings.Contains(output.String(), "from-flag") || !strings.Contains(output.String(), "temporary") {
		t.Fatalf("unexpected TUI output:\n%s", output.String())
	}
}
