package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// runCosmovisor runs cosmovisor as a child process instead of exec'ing it, so
// layerctl and its upgrade watcher stay alive. Signals are passed on to
// cosmovisor, lines matching LAYER_LOG_DROP are dropped, and layerctl exits
// with cosmovisor's exit code.
func runCosmovisor(args []string) error {
	path, err := exec.LookPath("cosmovisor")
	if err != nil {
		return fmt.Errorf("cosmovisor not found: %w", err)
	}

	home := env("LAYER_HOME", defaultHome)
	cmd := exec.Command(path, append([]string{"run"}, args...)...)
	cmd.Env = append(os.Environ(),
		"DAEMON_NAME=layerd",
		"DAEMON_HOME="+home,
		"DAEMON_RESTART_AFTER_UPGRADE=true",
		"DAEMON_ALLOW_DOWNLOAD_BINARIES=false",
	)
	cmd.Stdin = os.Stdin

	// Plain pipes instead of cmd.StdoutPipe, which Wait closes and could cut off
	// the last lines; these reach EOF only when every writer has closed them.
	outR, outW, err := os.Pipe()
	if err != nil {
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cosmovisor: %w", err)
	}
	outW.Close()
	errW.Close()

	drop := logDropPatterns()
	var dropped atomic.Int64
	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); copyFiltered(os.Stdout, outR, drop, &dropped) }()
	go func() { defer copies.Done(); copyFiltered(os.Stderr, errR, drop, &dropped) }()

	done := make(chan struct{})
	defer close(done)
	// Pass signals on; cosmovisor forwards them to layerd and decides how to stop.
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	if len(drop) > 0 {
		go reportDropped(done, &dropped, time.Hour)
	}

	waitErr := cmd.Wait()

	// Give the copiers a moment to flush what cosmovisor wrote last; bounded
	// because a process cosmovisor left behind may keep the pipes open.
	flushed := make(chan struct{})
	go func() { copies.Wait(); close(flushed) }()
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
	}

	// Exit with cosmovisor's status so Docker's restart policy sees the real
	// result; death by signal maps to 128+signal, as a shell reports it.
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		code := exitErr.ExitCode()
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
		os.Exit(code)
	}
	return waitErr
}

// logDropPatterns reads LAYER_LOG_DROP; unset or empty means no filtering. A
// standby node sets it to "not the primary signer", which it logs for every
// vote, proposal and oracle attestation it is not allowed to sign.
func logDropPatterns() [][]byte {
	var out [][]byte
	for _, p := range strings.Split(os.Getenv("LAYER_LOG_DROP"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, []byte(p))
		}
	}
	return out
}

// copyFiltered copies src to dst line by line, dropping lines that contain any
// pattern. It keeps reading even if writes to dst fail.
func copyFiltered(dst io.Writer, src io.Reader, drop [][]byte, dropped *atomic.Int64) {
	r := bufio.NewReaderSize(src, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if containsAny(line, drop) {
				dropped.Add(1)
			} else {
				_, _ = dst.Write(line)
			}
		}
		if err != nil {
			return
		}
	}
}

func containsAny(line []byte, patterns [][]byte) bool {
	for _, p := range patterns {
		if bytes.Contains(line, p) {
			return true
		}
	}
	return false
}

// reportDropped logs how many lines were dropped, so filtering is never silent.
func reportDropped(done <-chan struct{}, n *atomic.Int64, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if c := n.Swap(0); c > 0 {
				fmt.Fprintf(os.Stderr, "layerctl: dropped %d log lines matching LAYER_LOG_DROP in the last %s\n", c, every)
			}
		}
	}
}
