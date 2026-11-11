//go:build linux

// godeck-hub runs the single-process, single-owner signaling service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"godeck/internal/hubserver"
	"godeck/internal/hubstore"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func writeSetupCodeFile(path string, code string) error {
	cleanPath := filepath.Clean(path)
	parentDir := filepath.Dir(cleanPath)

	dirInfo, err := os.Lstat(parentDir)
	if err != nil {
		return fmt.Errorf("setup code directory stat failed: %w", err)
	}
	if !dirInfo.IsDir() {
		return fmt.Errorf("setup code directory %q is not a directory", parentDir)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("setup code directory %q cannot be a symlink", parentDir)
	}
	// Parent dir must be private: only owner has access (mode 0700)
	if dirInfo.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("setup code directory %q must be mode 0700 (got %04o)", parentDir, dirInfo.Mode().Perm())
	}

	// Check destination file if it already exists
	fileInfo, err := os.Lstat(cleanPath)
	if err == nil {
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("setup code file %q cannot be a symlink", cleanPath)
		}
		if !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("setup code file %q must be a regular file", cleanPath)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("setup code file stat failed: %w", err)
	}

	// Atomic safe write using a temp file in the same private parent directory
	tmpFile, err := os.CreateTemp(parentDir, ".setup-code-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp setup code file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to chmod temp setup code file: %w", err)
	}

	if _, err := io.WriteString(tmpFile, code+"\n"); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write setup code: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp setup code file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp setup code file: %w", err)
	}

	if err := os.Rename(tmpName, cleanPath); err != nil {
		return fmt.Errorf("failed to atomically replace setup code file: %w", err)
	}

	// Sync parent directory to persist the directory entry
	if d, err := os.Open(parentDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

func run() error {
	addr := flag.String("listen", "127.0.0.1:8787", "listen address")
	dir := flag.String("data", "./hub-data", "private state directory")
	cert := flag.String("tls-cert", "", "TLS certificate PEM")
	key := flag.String("tls-key", "", "TLS key PEM")
	insecure := flag.Bool("allow-insecure", false, "allow cleartext on a non-loopback interface")
	stun := flag.String("stun", "", "comma-separated STUN URLs")
	turn := flag.String("turn", "", "comma-separated TURN URLs")
	turnSecretFile := flag.String("turn-secret-file", "", "private file containing coturn shared secret")
	setupCodeFile := flag.String("setup-code-file", "", "private file to securely output one-time setup code for non-interactive daemon startup")
	flag.Parse()

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if (*cert == "") != (*key == "") {
		return errors.New("both TLS certificate and key are required")
	}
	if *cert == "" && !loopback && !*insecure {
		return errors.New("cleartext non-loopback listen requires -allow-insecure")
	}

	store, err := hubstore.Open(*dir)
	if err != nil {
		return err
	}
	defer store.Close()

	snap, err := store.Read()
	if err != nil {
		return err
	}

	cfg := hubserver.Config{}

	if snap.Owner == nil {
		code, err := hubstore.RandomToken()
		if err != nil {
			return fmt.Errorf("failed to generate setup code: %w", err)
		}
		cfg.SetupCode = code
		cfg.SetupUntil = time.Now().Add(15 * time.Minute)

		if *setupCodeFile != "" {
			if err := writeSetupCodeFile(*setupCodeFile, code); err != nil {
				// Fail-closed: do NOT print code to log or stdout
				return fmt.Errorf("failed to write setup code file: %w", err)
			}
		} else {
			info, e := os.Stdout.Stat()
			if e != nil || info.Mode()&os.ModeCharDevice == 0 {
				return errors.New("first startup requires a local terminal to display setup code or -setup-code-file")
			}
			fmt.Fprintf(os.Stdout, "One-time setup code (expires in 15 minutes): %s\n", cfg.SetupCode)
		}
	} else {
		// Already initialized: do not generate code.
		// If a stale setup-code file exists from a prior run, remove it.
		if *setupCodeFile != "" {
			_ = os.Remove(*setupCodeFile)
		}
	}

	split := func(v string) []string {
		var out []string
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	cfg.STUNURLs = split(*stun)
	cfg.TURNURLs = split(*turn)

	if len(cfg.TURNURLs) > 0 {
		if *turnSecretFile == "" {
			return errors.New("TURN requires -turn-secret-file")
		}
		info, e := os.Stat(*turnSecretFile)
		if e != nil {
			return e
		}
		if info.Mode().Perm()&0077 != 0 {
			return errors.New("TURN secret file must be private (mode 0600)")
		}
		b, e := os.ReadFile(*turnSecretFile)
		if e != nil {
			return e
		}
		cfg.TURNSecret = strings.TrimSpace(string(b))
		if len(cfg.TURNSecret) < 16 {
			return errors.New("TURN secret too short (minimum 16 bytes)")
		}
	}

	api := hubserver.New(store, cfg)
	server := &http.Server{
		Addr:              *addr,
		Handler:           api,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       40 * time.Second,
		WriteTimeout:      40 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan error, 1)
	go func() {
		if *cert != "" {
			done <- server.ServeTLS(listener, *cert, *key)
		} else {
			done <- server.Serve(listener)
		}
	}()

	select {
	case err = <-done:
		api.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		api.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	}
}
