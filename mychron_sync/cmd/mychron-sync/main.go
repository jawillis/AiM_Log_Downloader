// SPDX-License-Identifier: GPL-3.0-or-later

// mychron-sync downloads new sessions from an AiM logger whenever it appears
// on the network, and serves a small status page. It is built to run as a Home
// Assistant add-on but also works standalone (`mychron-sync serve -host ...`).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mychron-sync/internal/aim"
	"mychron-sync/internal/engine"
	"mychron-sync/internal/ha"
	"mychron-sync/internal/store"
	"mychron-sync/internal/web"
)

var version = "dev" // set at build time with -ldflags "-X main.version=..."

const optionsPath = "/data/options.json" // written by the Home Assistant Supervisor

// options mirrors the add-on's config.yaml options.
type options struct {
	LoggerHost            string `json:"logger_host"`
	PollIntervalSeconds   int    `json:"poll_interval_seconds"`
	ResyncIntervalMinutes int    `json:"resync_interval_minutes"`
	SettleSeconds         int    `json:"settle_seconds"`
	Probe                 string `json:"probe"`
	OutputDir             string `json:"output_dir"`
	KeepRaw               bool   `json:"keep_raw"`
	LogLevel              string `json:"log_level"`
}

func defaultOptions() options {
	return options{
		PollIntervalSeconds: 15, ResyncIntervalMinutes: 5, SettleSeconds: 5,
		Probe: engine.ProbeAuto, OutputDir: "/share/datalogger", LogLevel: "info",
	}
}

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "list":
		err = list(args)
	case "get":
		err = get(args)
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprintf(os.Stderr, "usage: mychron-sync [serve|list|get|version] [flags]\n")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", "", "logger IP address (overrides the add-on option)")
	out := fs.String("out", "", "output directory (overrides the add-on option)")
	listen := fs.String("listen", "", "address for the status page (default :8099 in Home Assistant, else 127.0.0.1:8099)")
	allowAny := fs.Bool("allow-any-client", false, "accept web requests from any address (local development only)")
	fs.Parse(args)

	opts := defaultOptions()
	inHA := os.Getenv("SUPERVISOR_TOKEN") != ""
	if b, err := os.ReadFile(optionsPath); err == nil {
		if err := json.Unmarshal(b, &opts); err != nil {
			return fmt.Errorf("reading %s: %w", optionsPath, err)
		}
	}
	if *host != "" {
		opts.LoggerHost = *host
	}
	if *out != "" {
		opts.OutputDir = *out
	}
	addr := *listen
	if addr == "" {
		addr = "127.0.0.1:8099"
		if inHA {
			addr = ":8099"
		}
	}
	log := newLogger(opts.LogLevel)
	opts.LoggerHost = strings.TrimSpace(opts.LoggerHost)

	man, err := store.Open(filepath.Join(opts.OutputDir, ".mychron-sync", "manifest.json"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return err
	}

	var notifier engine.Notifier
	if h := ha.New(log); h != nil {
		notifier = h
		log.Info("publishing status to Home Assistant", "entities", []string{ha.StatusEntity, ha.SessionsEntity})
	}
	eng := engine.New(engine.Config{
		Host:           opts.LoggerHost,
		PollInterval:   time.Duration(opts.PollIntervalSeconds) * time.Second,
		ResyncInterval: time.Duration(opts.ResyncIntervalMinutes) * time.Minute,
		Settle:         time.Duration(opts.SettleSeconds) * time.Second,
		OutputDir:      opts.OutputDir,
		KeepRaw:        opts.KeepRaw,
		Dial: func(ctx context.Context, h string) (engine.Device, error) {
			c, err := aim.Dial(ctx, aim.Options{Host: h, Keepalive: true, Log: log})
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		ProbeFn: engine.NewProbe(opts.Probe),
		Notify:  notifier,
		Log:     log,
	}, man)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go eng.Run(ctx)

	srv := &http.Server{
		Addr:              addr,
		Handler:           web.New(web.Deps{Engine: eng, Manifest: man, Version: version, AllowAny: *allowAny}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Info("starting", "version", version, "logger", opts.LoggerHost, "output", opts.OutputDir, "listen", addr, "probe", opts.Probe)
	if opts.LoggerHost == "" {
		log.Warn("logger_host is not set; nothing will be downloaded until it is")
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---- diagnostics: run these against the logger without the add-on loop ----

func dialFlags(name string, args []string) (*flag.FlagSet, *string, func() (*aim.Client, error)) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	host := fs.String("host", "", "logger IP address (required)")
	dial := func() (*aim.Client, error) {
		if *host == "" {
			return nil, errors.New("-host is required")
		}
		return aim.Dial(context.Background(), aim.Options{Host: *host, Keepalive: true})
	}
	return fs, host, dial
}

func list(args []string) error {
	fs, _, dial := dialFlags("list", args)
	fs.Parse(args)
	c, err := dial()
	if err != nil {
		return err
	}
	defer c.Close()
	sessions, err := c.ListSessions()
	if err != nil {
		return err
	}
	fmt.Printf("%d sessions\n", len(sessions))
	for _, s := range sessions {
		fmt.Printf("  %-16s %9d  %s %s  laps=%s  %s\n", s.Name, s.Size, s.Date, s.Hour, s.Laps, s.Track)
	}
	return nil
}

func get(args []string) error {
	fs, _, dial := dialFlags("get", args)
	outPath := fs.String("o", "", "output file (default: <name>.xrk)")
	raw := fs.Bool("raw", false, "save the download exactly as received, without inflating")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: mychron-sync get -host IP [-o out.xrk] [-raw] a_0775.xrz")
	}
	name := fs.Arg(0)
	c, err := dial()
	if err != nil {
		return err
	}
	defer c.Close()
	data, err := c.ReadFile("1:/mem/"+name, nil)
	if err != nil {
		return err
	}
	dest := *outPath
	if !*raw {
		if data, err = aim.Inflate(data); err != nil {
			return err
		}
		if dest == "" {
			dest = strings.TrimSuffix(name, filepath.Ext(name)) + ".xrk"
		}
	} else if dest == "" {
		dest = name
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d bytes to %s\n", len(data), dest)
	return nil
}
