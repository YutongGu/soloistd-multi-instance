// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

var updateCheckInterval = 6 * time.Hour

type instanceResult struct {
	generation uint64
	name       string
	code       int
	err        error
	updated    bool
}

func (a app) runDaemon(ctx context.Context, args []string) error {
	a = a.synchronizedWriters()
	if len(args) != 0 {
		return &commandError{code: 2, err: errors.New("run takes no arguments")}
	}
	p, err := resolvePaths()
	if err != nil {
		return err
	}
	cfg, err := loadConfigOptional(p)
	if err != nil {
		return err
	}
	instances := cfg.activeInstances(p)
	if err := validateInstancePaths(instances, p); err != nil {
		return err
	}
	if len(instances) > 0 {
		if err := a.ensureBinary(ctx, p); err != nil {
			return err
		}
		if _, err := a.checkForUpdate(ctx, p); err != nil {
			fmt.Fprintf(a.stderr, "soloistd: update check failed; continuing with installed build: %v\n", err)
		}
	}
	daemonCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := a.watchForUpdates(daemonCtx)
	requests := make(chan controlRequest)
	closeControl, err := listenForControl(daemonCtx, p, requests)
	if err != nil {
		return err
	}
	defer closeControl()
	results := make(chan instanceResult, len(instances))
	signals := make(map[string]chan struct{}, len(instances))
	active := make(map[string]bool, len(instances))
	cancels := make(map[string]context.CancelFunc, len(instances))
	removing := make(map[string]chan instanceResponse)
	restartReason := make(map[string]string)
	temporary := make(map[string]config)
	byName := make(map[string]config, len(instances))
	var running int
	var generation uint64
	start := func(inst config) {
		name := inst.Name
		if name == "" {
			name = "default"
		}
		ch := make(chan struct{}, 1)
		workerCtx, workerCancel := context.WithCancel(daemonCtx)
		cancels[name] = workerCancel
		signals[name] = ch
		active[name] = true
		running++
		startedGeneration := generation
		go func() {
			code, err, updated := a.runSoloistOnce(workerCtx, p.soloistBinary(), inst.soloistArgs(p), ch)
			results <- instanceResult{name: name, generation: startedGeneration, code: code, err: err, updated: updated}
		}()
	}
	for _, inst := range instances {
		name := inst.Name
		if name == "" {
			name = "default"
		}
		byName[name] = inst
		start(inst)
	}
	drain := func() {
		for running > 0 {
			<-results
			running--
		}
	}
	broadcast := func() {
		for name, ch := range signals {
			if active[name] {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}
	var lastExpiryRefresh time.Time
	for {
		select {
		case <-ctx.Done():
			cancel()
			drain()
			return nil
		case <-updates:
			if running == 0 {
				continue
			}
			updated, err := a.checkForUpdate(daemonCtx, p)
			if err != nil {
				fmt.Fprintf(a.stderr, "soloistd: update check failed; continuing with installed build: %v\n", err)
			} else if updated {
				generation++
				broadcast()
			}
		case request := <-requests:
			command := request.command
			respond := func(err error) {
				if err != nil {
					request.reply <- instanceResponse{Error: err.Error()}
				} else {
					request.reply <- instanceResponse{}
				}
			}
			switch command.Action {
			case "set-key":
				key := strings.TrimSpace(command.Instance.APIKey)
				if key == "" {
					respond(errors.New("API key cannot be empty"))
					continue
				}
				next := cfg
				next.APIKey = key
				if err := validateConfig(next); err != nil {
					respond(err)
					continue
				}
				if err := saveConfig(p, next); err != nil {
					respond(err)
					continue
				}
				changed := cfg.APIKey != key
				cfg = next
				if changed {
					for _, inst := range cfg.activeInstances(p) {
						name := inst.Name
						if name == "" {
							name = "default"
						}
						if !active[name] {
							continue
						}
						if name != "default" {
							inherited := false
							for _, saved := range cfg.Instances {
								if saved.Name == name && saved.APIKey == "" {
									inherited = true
									break
								}
							}
							if !inherited {
								continue
							}
						}
						byName[name] = inst
						restartReason[name] = "API key change"
						select {
						case signals[name] <- struct{}{}:
						default:
						}
					}
					for name, raw := range temporary {
						if raw.APIKey != "" || !active[name] {
							continue
						}
						inst, err := resolveTemporaryInstance(raw, cfg.APIKey, p)
						if err != nil {
							continue
						}
						byName[name] = inst
						restartReason[name] = "API key change"
						select {
						case signals[name] <- struct{}{}:
						default:
						}
					}
				}
				respond(nil)
			case "list":
				info := make([]instanceInfo, 0, len(cfg.Instances)+len(temporary)+1)
				for _, inst := range cfg.activeInstances(p) {
					name := inst.Name
					if name == "" {
						name = "default"
					}
					info = append(info, instanceInfo{Name: name, DeviceName: inst.DeviceName, PipewireDevice: inst.PipewireDevice})
				}
				names := make([]string, 0, len(temporary))
				for name := range temporary {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					inst := temporary[name]
					info = append(info, instanceInfo{Name: name, DeviceName: inst.DeviceName, PipewireDevice: inst.PipewireDevice, NoPersist: true})
				}
				request.reply <- instanceResponse{Instances: info}
			case "add", "add-temporary":
				inst := command.Instance
				name := inst.Name
				if name == "" || (name != "default" && !instanceNamePattern.MatchString(name)) {
					respond(fmt.Errorf("invalid instance name %q", name))
					continue
				}
				if active[name] {
					respond(fmt.Errorf("instance %q already exists", name))
					continue
				}
				if command.Action == "add-temporary" {
					selected, err := resolveTemporaryInstance(inst, cfg.APIKey, p)
					if err != nil {
						respond(err)
						continue
					}
					resolvedTemporary, err := resolveTemporaryInstances(temporary, cfg.APIKey, p)
					if err != nil {
						respond(err)
						continue
					}
					resolved := append(cfg.activeInstances(p), resolvedTemporary...)
					resolved = append(resolved, selected)
					if err := validateInstancePaths(resolved, p); err != nil {
						respond(err)
						continue
					}
					if running == 0 {
						if err := a.ensureBinary(daemonCtx, p); err != nil {
							respond(err)
							continue
						}
						if _, err := a.checkForUpdate(daemonCtx, p); err != nil {
							fmt.Fprintf(a.stderr, "soloistd: update check failed; continuing with installed build: %v\n", err)
						}
					}
					temporary[name] = inst
					byName[name] = selected
					start(selected)
					fmt.Fprintf(a.stdout, "Started temporary Soloist instance %s\n", name)
					respond(nil)
					continue
				}
				next := cfg
				if name == "default" {
					if next.DeviceName != "" {
						respond(fmt.Errorf("instance %q already exists", name))
						continue
					}
					next.DeviceName, next.PipewireDevice = inst.DeviceName, inst.PipewireDevice
					if inst.APIKey != "" {
						next.APIKey = inst.APIKey
					}
				} else {
					if next.APIKey == "" && inst.APIKey != "" {
						next.APIKey, inst.APIKey = inst.APIKey, ""
					}
					next.Instances = append(append([]config(nil), cfg.Instances...), inst)
				}
				if err := validateConfig(next); err != nil {
					respond(err)
					continue
				}
				resolvedTemporary, err := resolveTemporaryInstances(temporary, next.APIKey, p)
				if err != nil {
					respond(err)
					continue
				}
				resolved := append(next.activeInstances(p), resolvedTemporary...)
				if err := validateInstancePaths(resolved, p); err != nil {
					respond(err)
					continue
				}
				if running == 0 {
					if err := a.ensureBinary(daemonCtx, p); err != nil {
						respond(err)
						continue
					}
					if _, err := a.checkForUpdate(daemonCtx, p); err != nil {
						fmt.Fprintf(a.stderr, "soloistd: update check failed; continuing with installed build: %v\n", err)
					}
				}
				if err := saveConfig(p, next); err != nil {
					respond(err)
					continue
				}
				cfg = next
				selected, err := cfg.instance(name, p)
				if err != nil {
					respond(err)
					continue
				}
				byName[name] = selected
				start(selected)
				fmt.Fprintf(a.stdout, "Started Soloist instance %s\n", name)
				respond(nil)
			case "remove":
				name := command.Name
				if !active[name] {
					respond(fmt.Errorf("unknown instance %q", name))
					continue
				}
				if _, ok := temporary[name]; ok {
					delete(temporary, name)
					removing[name] = request.reply
					cancels[name]()
					continue
				}
				next := cfg
				if name == "default" {
					next.DeviceName = ""
					next.PipewireDevice = ""
					next.DataDir = ""
					next.CacheDir = ""
					next.CacheSizeMB = nil
					next.InitialVolume = nil
					next.WebSocket = ""
				} else {
					next.Instances = make([]config, 0, len(cfg.Instances)-1)
					for _, inst := range cfg.Instances {
						if inst.Name != name {
							next.Instances = append(next.Instances, inst)
						}
					}
				}
				if err := saveConfig(p, next); err != nil {
					respond(err)
					continue
				}
				cfg = next
				removing[name] = request.reply
				cancels[name]()
			default:
				respond(fmt.Errorf("unknown instance action %q", command.Action))
			}
		case result := <-results:
			running--
			active[result.name] = false
			cancels[result.name]()
			delete(cancels, result.name)
			if reply, ok := removing[result.name]; ok {
				delete(removing, result.name)
				delete(signals, result.name)
				delete(byName, result.name)
				delete(restartReason, result.name)
				reply <- instanceResponse{}
				fmt.Fprintf(a.stdout, "Stopped Soloist instance %s\n", result.name)
				continue
			}
			if ctx.Err() != nil {
				cancel()
				drain()
				return nil
			}
			if result.updated || result.generation < generation {
				if reason := restartReason[result.name]; reason != "" {
					fmt.Fprintf(a.stdout, "Restarting Soloist instance %s after %s...\n", result.name, reason)
					delete(restartReason, result.name)
				} else {
					fmt.Fprintf(a.stdout, "Restarting Soloist with the new build... (instance %s)\n", result.name)
				}
				start(byName[result.name])
				continue
			}
			if result.code == 10 {
				if !lastExpiryRefresh.IsZero() && time.Since(lastExpiryRefresh) < 5*time.Minute {
					cancel()
					drain()
					return errors.New("freshly downloaded Soloist build also reported expiry")
				}
				fmt.Fprintf(a.stderr, "Soloist instance %s build expired (exit 10); downloading the current build...\n", result.name)
				if err := a.installLatest(ctx, p); err != nil {
					cancel()
					drain()
					return fmt.Errorf("refresh expired Soloist build: %w", err)
				}
				lastExpiryRefresh = time.Now()
				generation++
				broadcast()
				fmt.Fprintf(a.stdout, "Restarting Soloist with the new build... (instance %s)\n", result.name)
				start(byName[result.name])
				continue
			}
			cancel()
			drain()
			if result.err == nil {
				return nil
			}
			return &commandError{code: result.code, err: fmt.Errorf("Soloist instance %s exited: %w", result.name, result.err)}
		}
	}
}

func validateInstancePaths(instances []config, p paths) error {
	dataDirs := map[string]string{}
	cacheDirs := map[string]string{}
	check := func(name, field, dir string, seen map[string]string) error {
		if dir == "" {
			return fmt.Errorf("instance %s has empty %s", name, field)
		}
		dir = filepath.Clean(dir)
		for otherDir, other := range seen {
			if pathContains(dir, otherDir) || pathContains(otherDir, dir) {
				return fmt.Errorf("instances %s and %s have overlapping %s paths", other, name, field)
			}
		}
		seen[dir] = name
		return nil
	}
	for _, inst := range instances {
		name := inst.Name
		if name == "" {
			name = "default"
		}
		dataDir := inst.DataDir
		if dataDir == "" {
			dataDir = p.soloistData
		}
		cacheDir := inst.CacheDir
		if cacheDir == "" {
			cacheDir = p.soloistCache
		}
		if err := check(name, "data_dir", dataDir, dataDirs); err != nil {
			return err
		}
		if err := check(name, "cache_dir", cacheDir, cacheDirs); err != nil {
			return err
		}
	}
	return nil
}

func resolveTemporaryInstance(raw config, sharedKey string, p paths) (config, error) {
	if len(raw.Instances) != 0 {
		return config{}, errors.New("nested instances are not supported")
	}
	if raw.Name == "default" {
		raw.Name = ""
		if raw.APIKey == "" {
			raw.APIKey = sharedKey
		}
		if err := validateInstance(raw); err != nil {
			return config{}, err
		}
		return raw, nil
	}
	probe := config{APIKey: sharedKey, Instances: []config{raw}}
	if err := validateConfig(probe); err != nil {
		return config{}, err
	}
	return probe.activeInstances(p)[0], nil
}

func resolveTemporaryInstances(temporary map[string]config, sharedKey string, p paths) ([]config, error) {
	result := make([]config, 0, len(temporary))
	for _, raw := range temporary {
		inst, err := resolveTemporaryInstance(raw, sharedKey, p)
		if err != nil {
			return nil, err
		}
		result = append(result, inst)
	}
	return result, nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func (a app) watchForUpdates(ctx context.Context) <-chan struct{} {
	ticks := make(chan struct{}, 1)
	go func() {
		ticker := time.NewTicker(updateCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case ticks <- struct{}{}:
				default:
				}
			}
		}
	}()
	return ticks
}

func (a app) runSoloistOnce(ctx context.Context, binary string, args []string, updates <-chan struct{}) (int, error, bool) {
	cmd := exec.Command(binary, args...)
	cmd.Stdout, cmd.Stderr = a.stdout, a.stderr
	if err := cmd.Start(); err != nil {
		return 1, err, false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	for {
		select {
		case err := <-done:
			return exitStatus(err), err, false
		case <-updates:
			return stopSoloist(cmd, done, nil, true)
		case <-ctx.Done():
			return stopSoloist(cmd, done, ctx.Err(), false)
		}
	}
}

func stopSoloist(cmd *exec.Cmd, done <-chan error, returnErr error, updated bool) (int, error, bool) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return 0, returnErr, updated
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return 0, returnErr, updated
	}
}
