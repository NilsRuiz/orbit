// orbit-agent runs on every edge node. It watches the node's desired state, converges the
// payload to it, and rolls back locally if the new version is not healthy in time.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NilsRuiz/orbit/agent/internal/config"
	"github.com/NilsRuiz/orbit/agent/internal/payload"
	"github.com/NilsRuiz/orbit/agent/internal/reconcile"
	"github.com/NilsRuiz/orbit/agent/internal/state"
	"github.com/NilsRuiz/orbit/agent/internal/transport"
)

func main() {
	if err := run(); err != nil {
		slog.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel).With("node", cfg.NodeID)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := state.NewStore(cfg.StateDir)
	if err != nil {
		return err
	}
	st, err := store.Load(cfg.InitialVersion)
	if err != nil {
		return err
	}
	log.Info("starting", "current", st.Current, "phase", st.Phase)

	nc, err := transport.Connect(cfg.NATSURL, cfg.NodeID, cfg.NATSCredsFile, cfg.JetStreamDomain, log)
	if err != nil {
		return err
	}
	defer nc.Close()

	sim := payload.Sim{ApplyDelay: 2 * time.Second, BadVersions: cfg.SimBadVersions}
	rec := reconcile.New(reconcile.Config{
		NodeID:         cfg.NodeID,
		VerifyTimeout:  cfg.VerifyTimeout,
		VerifyInterval: time.Second,
	}, st, store, sim, sim, nc, log)

	// Finish any transition interrupted by a crash/reboot before accepting new work.
	if err := rec.Resume(ctx); err != nil {
		return err
	}

	go heartbeat(ctx, nc, rec, cfg.NodeLabels, cfg.HeartbeatInterval, log)

	desired, err := nc.WatchDesired(ctx)
	if err != nil {
		return err
	}
	for d := range desired {
		if err := rec.Reconcile(ctx, d); err != nil && ctx.Err() == nil {
			log.Error("reconcile failed", "err", err)
		}
	}
	log.Info("shutting down")
	return nil
}

func heartbeat(ctx context.Context, nc *transport.NATS, rec *reconcile.Reconciler, labels map[string]string, every time.Duration, log *slog.Logger) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st := rec.State()
			hb := transport.Heartbeat{Current: st.Current, Phase: string(st.Phase), RolloutID: st.RolloutID, Labels: labels}
			if err := nc.Heartbeat(hb); err != nil {
				log.Debug("heartbeat not sent", "err", err)
			}
		}
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
