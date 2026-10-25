package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PrathamKapoor/ConvergeDB/internal/api"
	"github.com/PrathamKapoor/ConvergeDB/internal/node"
)

// fileConfig mirrors node.Config as a JSON config file (chosen over YAML to
// keep the standard library only — see decisions.md).
type fileConfig struct {
	ID             string            `json:"id"`
	DB             string            `json:"db"`
	HTTP           string            `json:"http"`
	Peers          []node.PeerConfig `json:"peers"`
	Lab            bool              `json:"lab"`
	Token          string            `json:"token,omitempty"`
	SyncIntervalMS int               `json:"sync_interval_ms"`
	FaultSeed      int64             `json:"fault_seed"`
	Web            string            `json:"web,omitempty"`
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "JSON config file (flags override it)")
	id := fs.String("id", "", "replica id (lowercase [a-z0-9-])")
	db := fs.String("db", "", "SQLite database path (one per replica)")
	httpAddr := fs.String("http", "127.0.0.1:8101", "listen address")
	peers := fs.String("peers", "", "comma list id=url[,id=url...]")
	lab := fs.Bool("lab", false, "enable lab fault-injection endpoints (localhost use only)")
	token := fs.String("token", "", "require this bearer token on all non-health endpoints")
	allowUnsafe := fs.Bool("allow-unsafe-bind", false, "permit non-loopback bind without a token (for containerized multi-node labs only; documented in docs/SECURITY.md)")
	interval := fs.Int("sync-interval-ms", 1000, "anti-entropy interval in milliseconds")
	seed := fs.Int64("fault-seed", 1, "seed for reproducible fault injection")
	web := fs.String("web", "", "serve the built web workbench from this directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := node.Config{SyncIntervalMS: 1000, FaultSeed: 1}
	if *cfgPath != "" {
		raw, err := os.ReadFile(*cfgPath)
		if err != nil {
			return fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("parse config %s: %w", *cfgPath, err)
		}
	}
	if *id != "" {
		cfg.ID = *id
	}
	if *db != "" {
		cfg.DBPath = *db
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	if visited["http"] {
		cfg.HTTPAddr = *httpAddr
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = *httpAddr
	}
	if *peers != "" {
		cfg.Peers = nil
		for _, part := range strings.Split(*peers, ",") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) != 2 {
				return fmt.Errorf("--peers expects id=url pairs, got %q", part)
			}
			cfg.Peers = append(cfg.Peers, node.PeerConfig{ID: kv[0], URL: kv[1]})
		}
	}
	if *lab {
		cfg.LabMode = true
	}
	if *token != "" {
		cfg.AuthToken = *token
	}
	if visited["sync-interval-ms"] {
		cfg.SyncIntervalMS = *interval
	}
	if visited["fault-seed"] {
		cfg.FaultSeed = *seed
	}
	if *web != "" {
		cfg.WebDir = *web
	}

	if cfg.ID == "" || cfg.DBPath == "" {
		return fmt.Errorf("serve requires --id and --db (or a --config file providing them)")
	}
	if !strings.HasPrefix(cfg.HTTPAddr, "127.0.0.1") && !strings.HasPrefix(cfg.HTTPAddr, "localhost") && !strings.HasPrefix(cfg.HTTPAddr, "[::1]") {
		// Safety baseline: refuse unsafe default public binding.
		if cfg.AuthToken == "" && !(*allowUnsafe) {
			return fmt.Errorf("refusing to bind %q without an auth token; ConvergeDB supports trusted-localhost by default, or pass --token for authenticated non-loopback use (containers may pass --allow-unsafe-bind)", cfg.HTTPAddr)
		}
	}

	n, err := node.Open(cfg)
	if err != nil {
		return err
	}
	defer n.Close()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewServer(n, cfg.WebDir),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,  // request bodies; SSE writes unaffected (no WriteTimeout on purpose)
		IdleTimeout:       120 * time.Second, // keep-alive connections
		MaxHeaderBytes:    16 << 10,
	}
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.HTTPAddr, err)
	}
	fmt.Printf("convergedb replica %s listening on http://%s (db=%s, peers=%d, lab=%v)\n",
		cfg.ID, cfg.HTTPAddr, cfg.DBPath, len(cfg.Peers), cfg.LabMode)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-stop:
		fmt.Printf("received %s, shutting down\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
