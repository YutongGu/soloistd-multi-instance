// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const invalidAPIKeyMessage = "login failed: make sure --api-key is valid"

// loginFailureWriter passes output through to the instance log while detecting
// the exact diagnostic emitted by Soloist. A bounded tail handles split writes.
type loginFailureWriter struct {
	mu     sync.Mutex
	output io.Writer
	tail   []byte
	failed *atomic.Bool
}

func (w *loginFailureWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.output.Write(data)
	if n > 0 {
		combined := append(append([]byte(nil), w.tail...), data[:n]...)
		if bytes.Contains(combined, []byte(invalidAPIKeyMessage)) {
			w.failed.Store(true)
		}
		if keep := len(invalidAPIKeyMessage) - 1; len(combined) > keep {
			combined = combined[len(combined)-keep:]
		}
		w.tail = combined
	}
	return n, err
}

func openInstanceLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("log path is not a regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func checkInstanceLog(path string) error {
	file, err := openInstanceLog(path)
	if err != nil {
		return fmt.Errorf("open Soloist log %s: %w", path, err)
	}
	return file.Close()
}
