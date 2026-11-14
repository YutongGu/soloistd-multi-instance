// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type instanceCommand struct {
	Action   string `json:"action"`
	Instance config `json:"instance,omitempty"`
	Name     string `json:"name,omitempty"`
}

type instanceInfo struct {
	Name           string `json:"name"`
	DeviceName     string `json:"device_name"`
	PipewireDevice string `json:"pipewire_device,omitempty"`
	NoPersist      bool   `json:"no_persist,omitempty"`
}

type instanceResponse struct {
	Error     string         `json:"error,omitempty"`
	Instances []instanceInfo `json:"instances,omitempty"`
}

type controlRequest struct {
	command instanceCommand
	reply   chan instanceResponse
}

func (p paths) controlSocket() string {
	return filepath.Join(filepath.Dir(p.configFile), "control.sock")
}

func listenForControl(ctx context.Context, p paths, requests chan<- controlRequest) (func(), error) {
	dir := filepath.Dir(p.controlSocket())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	socket := p.controlSocket()
	if _, err := os.Lstat(socket); err == nil {
		conn, dialErr := net.DialTimeout("unix", socket, 200*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return nil, errors.New("another soloistd supervisor is already running")
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		os.Remove(socket)
		return nil, err
	}
	closeListener := func() { listener.Close(); os.Remove(socket) }
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(6 * time.Minute))
				var command instanceCommand
				if err := json.NewDecoder(io.LimitReader(conn, 64*1024)).Decode(&command); err != nil {
					_ = json.NewEncoder(conn).Encode(instanceResponse{Error: "invalid request: " + err.Error()})
					return
				}
				reply := make(chan instanceResponse, 1)
				select {
				case requests <- controlRequest{command: command, reply: reply}:
				case <-ctx.Done():
					return
				}
				select {
				case response := <-reply:
					_ = json.NewEncoder(conn).Encode(response)
				case <-ctx.Done():
					return
				}
			}()
		}
	}()
	return closeListener, nil
}

func sendControl(ctx context.Context, p paths, command instanceCommand) (instanceResponse, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", p.controlSocket())
	if err != nil {
		return instanceResponse{}, fmt.Errorf("supervisor is not running; start it with 'soloistd service start' or 'soloistd run': %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(6 * time.Minute))
	if err := json.NewEncoder(conn).Encode(command); err != nil {
		return instanceResponse{}, err
	}
	var response instanceResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return instanceResponse{}, err
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func (a app) instanceCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &commandError{code: 2, err: errors.New("usage: soloistd instance add|remove|list")}
	}
	p, err := resolvePaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return &commandError{code: 2, err: errors.New("list takes no arguments")}
		}
		response, err := sendControl(ctx, p, instanceCommand{Action: "list"})
		if err != nil {
			return err
		}
		for _, inst := range response.Instances {
			destination := inst.PipewireDevice
			if inst.NoPersist {
				if destination == "" {
					destination = "(temporary)"
				} else {
					destination += " (temporary)"
				}
			}
			fmt.Fprintf(a.stdout, "%s\t%s\t%s\n", inst.Name, inst.DeviceName, destination)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("instance add", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		name := fs.String("name", "", "instance ID")
		device := fs.String("device-name", "", "Spotify Connect device name")
		pipewire := fs.String("pipewire-device", "", "PipeWire destination")
		key := fs.String("api-key", "", "Soloist API key (prefer SOLOIST_API_KEY)")
		noPersist := fs.Bool("no-persist", false, "run only until the supervisor restarts")
		if err := fs.Parse(args[1:]); err != nil {
			return &commandError{code: 2, err: err}
		}
		if fs.NArg() != 0 || *name == "" || *device == "" {
			return &commandError{code: 2, err: errors.New("usage: soloistd instance add --name ID --device-name NAME [--pipewire-device DEST] [--api-key KEY] [--no-persist]")}
		}
		if !instanceNamePattern.MatchString(*name) && *name != "default" {
			return &commandError{code: 2, err: fmt.Errorf("invalid instance name %q", *name)}
		}
		if *key == "" {
			*key = os.Getenv("SOLOIST_API_KEY")
		}
		if *key == "" {
			saved, err := loadConfigOptional(p)
			if err != nil {
				return err
			}
			if saved.APIKey == "" {
				if _, err := sendControl(ctx, p, instanceCommand{Action: "list"}); err != nil {
					return err
				}
				fmt.Fprint(a.stdout, "Soloist API key: ")
				if input, ok := isTerminalReader(a.stdin); ok {
					secret, err := readPasswordContext(ctx, input)
					fmt.Fprintln(a.stdout)
					if err != nil {
						return err
					}
					*key = string(secret)
				} else {
					line, err := readLineContext(ctx, bufio.NewReader(a.stdin))
					if err != nil && !errors.Is(err, io.EOF) {
						return err
					}
					*key = strings.TrimSpace(line)
				}
				if *key == "" {
					return errors.New("API key cannot be empty")
				}
			}
		}
		action := "add"
		if *noPersist {
			action = "add-temporary"
		}
		command := instanceCommand{Action: action, Instance: config{Name: *name, DeviceName: strings.TrimSpace(*device), PipewireDevice: *pipewire, APIKey: *key}}
		if _, err := sendControl(ctx, p, command); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Started instance %s\n", *name)
		return nil
	case "remove":
		fs := flag.NewFlagSet("instance remove", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		name := fs.String("name", "", "instance ID")
		if err := fs.Parse(args[1:]); err != nil {
			return &commandError{code: 2, err: err}
		}
		if fs.NArg() != 0 || *name == "" {
			return &commandError{code: 2, err: errors.New("usage: soloistd instance remove --name ID")}
		}
		if _, err := sendControl(ctx, p, instanceCommand{Action: "remove", Name: *name}); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "Stopped and removed instance %s (session and cache kept)\n", *name)
		return nil
	default:
		return &commandError{code: 2, err: fmt.Errorf("unknown instance action %q", args[0])}
	}
}
