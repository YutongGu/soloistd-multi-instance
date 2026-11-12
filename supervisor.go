// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
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
	p, cfg, err := configuredPaths()
	if err != nil {
		return err
	}
	instances := cfg.activeInstances(p)
	if err := validateInstancePaths(instances, p); err != nil {
		return err
	}
	if err := a.ensureBinary(ctx, p); err != nil {
		return err
	}
	if _, err := a.checkForUpdate(ctx, p); err != nil {
		fmt.Fprintf(a.stderr, "soloistd: update check failed; continuing with installed build: %v\n", err)
	}
	daemonCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := a.watchForUpdates(daemonCtx)
	results := make(chan instanceResult, len(instances))
	signals := make(map[string]chan struct{}, len(instances))
	active := make(map[string]bool, len(instances))
	byName := make(map[string]config, len(instances))
	var running int
	var generation uint64
	start := func(inst config) {
		name := inst.Name
		if name == "" {
			name = "default"
		}
		ch := make(chan struct{}, 1)
		signals[name] = ch
		active[name] = true
		running++
		startedGeneration := generation
		go func() {
			code, err, updated := a.runSoloistOnce(daemonCtx, p.soloistBinary(), inst.soloistArgs(p), ch)
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
			updated, err := a.checkForUpdate(daemonCtx, p)
			if err != nil {
				fmt.Fprintf(a.stderr, "soloistd: update check failed; continuing with installed build: %v\n", err)
			} else if updated {
				generation++
				broadcast()
			}
		case result := <-results:
			running--
			active[result.name] = false
			if ctx.Err() != nil {
				cancel()
				drain()
				return nil
			}
			if result.updated || result.generation < generation {
				fmt.Fprintf(a.stdout, "Restarting Soloist with the new build... (instance %s)\n", result.name)
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
