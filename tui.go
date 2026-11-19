// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// tui is a small interactive front end to the running supervisor's control socket.
func (a app) tui(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	apiKey := fs.String("api-key", "", "default API key for instances added in this session")
	noPersist := fs.Bool("no-persist", false, "do not restore instances added in this session after a supervisor restart")
	monitorAPIKey := fs.Bool("monitor-api-key", false, "detect API-key login failures for instances added in this session")
	if err := fs.Parse(args); err != nil {
		return &commandError{code: 2, err: err}
	}
	if fs.NArg() != 0 {
		return &commandError{code: 2, err: errors.New("tui takes no positional arguments")}
	}
	keyFlag := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "api-key" {
			keyFlag = true
		}
	})
	if keyFlag {
		key, err := nonEmpty(*apiKey, "API key")
		if err != nil {
			return &commandError{code: 2, err: err}
		}
		*apiKey = key
	}
	sessionKey := *apiKey
	if !keyFlag {
		sessionKey = os.Getenv("SOLOIST_API_KEY")
	}
	p, err := resolvePaths()
	if err != nil {
		return err
	}
	reader := bufio.NewReader(a.stdin)
	prompt := func(label string) (string, error) {
		fmt.Fprint(a.stdout, label)
		line, err := readLineContext(ctx, reader)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if errors.Is(err, io.EOF) && line == "" {
			return "", io.EOF
		}
		return strings.TrimSpace(line), nil
	}
	show := func() error {
		response, err := sendControl(ctx, p, instanceCommand{Action: "list"})
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, "\nSoloist instances:")
		if len(response.Instances) == 0 {
			fmt.Fprintln(a.stdout, "  (none)")
			return nil
		}
		for _, inst := range response.Instances {
			destination := inst.PipewireDevice
			if destination == "" {
				destination = "default PipeWire output"
			}
			if inst.NoPersist {
				destination += "; temporary"
			}
			fmt.Fprintf(a.stdout, "  %s — %s (%s)\n    status: %s\n    log: %s\n", inst.Name, inst.DeviceName, destination, inst.Status, inst.LogPath)
		}
		return nil
	}
	if err := show(); err != nil {
		return err
	}
	for {
		fmt.Fprintln(a.stdout, "\n[a] Add  [r] Remove  [v] Refresh  [q] Quit")
		choice, err := prompt("> ")
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch strings.ToLower(choice) {
		case "a", "add":
			name, err := prompt("Instance ID: ")
			if err != nil {
				return err
			}
			if (!instanceNamePattern.MatchString(name) && name != "default") || name == "" {
				fmt.Fprintln(a.stdout, "Invalid ID: use letters, digits, _ or -, starting with a letter or digit.")
				continue
			}
			device, err := prompt(fmt.Sprintf("Spotify device name [%s]: ", name))
			if err != nil {
				return err
			}
			if device == "" {
				device = name
			}
			logPath, err := prompt("Log path [default]: ")
			if err != nil {
				return err
			}
			cfg, err := loadConfigOptional(p)
			if err != nil {
				return err
			}
			key := sessionKey
			if key == "" {
				if cfg.APIKey == "" {
					fmt.Fprint(a.stdout, "Soloist API key: ")
					if input, ok := isTerminalReader(a.stdin); ok {
						secret, err := readPasswordContext(ctx, input)
						fmt.Fprintln(a.stdout)
						if err != nil {
							return err
						}
						key = string(secret)
					} else {
						key, err = prompt("")
						if err != nil {
							return err
						}
					}
					if key == "" {
						fmt.Fprintln(a.stdout, "API key cannot be empty.")
						continue
					}
				}
			}
			if key == cfg.APIKey {
				key = ""
			}
			action := "add"
			if *noPersist {
				action = "add-temporary"
			}
			_, err = sendControl(ctx, p, instanceCommand{Action: action, Instance: config{Name: name, DeviceName: device, LogPath: logPath, MonitorAPIKey: *monitorAPIKey, APIKey: key}})
			if err != nil {
				fmt.Fprintf(a.stdout, "Could not add instance: %v\n", err)
				continue
			}
			if err := show(); err != nil {
				return err
			}
		case "r", "remove":
			name, err := prompt("Instance ID to remove: ")
			if err != nil {
				return err
			}
			if name == "" {
				continue
			}
			_, err = sendControl(ctx, p, instanceCommand{Action: "remove", Name: name})
			if err != nil {
				fmt.Fprintf(a.stdout, "Could not remove instance: %v\n", err)
				continue
			}
			if err := show(); err != nil {
				return err
			}
		case "v", "view", "refresh":
			if err := show(); err != nil {
				return err
			}
		case "q", "quit", "exit":
			return nil
		default:
			fmt.Fprintln(a.stdout, "Choose a, r, v, or q.")
		}
	}
}
