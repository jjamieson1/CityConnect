// Command server runs the CityConnect API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jjamieson1/CityConnect/internal/agents"
	"github.com/jjamieson1/CityConnect/internal/audit"
	"github.com/jjamieson1/CityConnect/internal/c2/callout"
	"github.com/jjamieson1/CityConnect/internal/c2/notify"
	"github.com/jjamieson1/CityConnect/internal/c2/oidc"
	"github.com/jjamieson1/CityConnect/internal/catalog"
	"github.com/jjamieson1/CityConnect/internal/config"
	"github.com/jjamieson1/CityConnect/internal/contacts"
	"github.com/jjamieson1/CityConnect/internal/httpapi"
	"github.com/jjamieson1/CityConnect/internal/interactions"
	"github.com/jjamieson1/CityConnect/internal/jobs"
	"github.com/jjamieson1/CityConnect/internal/notifications"
	"github.com/jjamieson1/CityConnect/internal/portal"
	"github.com/jjamieson1/CityConnect/internal/reports"
	"github.com/jjamieson1/CityConnect/internal/requests"
	"github.com/jjamieson1/CityConnect/internal/routing"
	"github.com/jjamieson1/CityConnect/internal/seed"
	"github.com/jjamieson1/CityConnect/internal/store"
	"github.com/jjamieson1/CityConnect/internal/webhooks"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting CityConnect",
		"env", cfg.Env, "addr", cfg.Addr, "base_path", cfg.BasePath,
		"c2_issuer", cfg.C2.Issuer)

	// The two-origin split, and the rule that C2 only ever talks to the public
	// host, both assume the API itself is not directly reachable. If it is,
	// say so — this is a deliberate choice in a container, and an oversight
	// on a host running Apache.
	if cfg.IsProd() && cfg.PubliclyBound() {
		log.Warn("the API is listening on all interfaces",
			"addr", cfg.Addr,
			"expected", "127.0.0.1:4021 behind Apache",
			"why", "everything reaches CityConnect through the reverse proxy; "+
				"a directly reachable API bypasses the per-origin rules that separate "+
				"the staff console from the public portal",
			"if_intended", "set CC_ADDR explicitly, e.g. in a container or behind an external load balancer")
	}

	db, err := store.Open(cfg.DB, log)
	if err != nil {
		return err
	}
	if cfg.DB.AutoMigrate {
		log.Info("running AutoMigrate (development mode)")
		if err := store.Migrate(db, log); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Services are wired bottom-up: each takes only what it genuinely needs,
	// and the two cycles (requests↔notifications, requests↔webhooks) are
	// broken by setters rather than by merging packages.
	auditSvc := audit.NewService(db, log)
	provider := oidc.New(cfg.C2)

	agentSvc := agents.NewService(db, cfg, provider, auditSvc, log)
	contactSvc := contacts.NewService(db, auditSvc, log)
	interactionSvc := interactions.NewService(db, auditSvc, log)
	catalogSvc := catalog.NewService(db, auditSvc, log)
	routingSvc := routing.NewService(db, auditSvc, log)
	requestSvc := requests.NewService(db, auditSvc, catalogSvc, routingSvc, log)
	webhookSvc := webhooks.NewService(db, auditSvc, log)
	reportSvc := reports.NewService(db, log)

	notifyClient, err := notify.New(cfg.C2)
	if err != nil {
		// A deployment without notification credentials is still useful — the
		// console works, requests flow — so this degrades rather than refuses
		// to boot, but it must be loudly visible.
		log.Warn("citizen notifications are disabled", "reason", err)
	}

	var notificationSvc *notifications.Service
	if notifyClient != nil {
		notificationSvc = notifications.NewService(db, cfg, notifyClient, catalogSvc, contactSvc, auditSvc, log)
		requestSvc.SetNotifier(notificationSvc)
		log.Info("citizen notifications enabled",
			"auth", authMode(notifyClient), "endpoint", cfg.C2.PartnerNotificationsURL())
	}
	requestSvc.SetWebhooks(webhookSvc)

	calloutSvc := callout.NewService(db, cfg, provider, contactSvc, requestSvc, log)
	portalSvc := portal.NewService(db, cfg, provider, contactSvc, catalogSvc, requestSvc, auditSvc, log)

	attachments, err := requests.NewAttachmentStore(cfg.AttachmentDir, cfg.AttachmentMaxMB, nil)
	if err != nil {
		return err
	}

	if err := seed.Run(ctx, db, cfg, log); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	if err := agentSvc.Bootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap admins: %w", err)
	}

	// Resolve C2 discovery at boot so a misconfiguration is visible in the
	// startup log rather than on a user's first sign-in attempt.
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := provider.Check(checkCtx); err != nil {
		log.Warn("C2 is not reachable; staff sign-in will fail until it is",
			"error", err, "issuer", cfg.C2.Issuer,
			"hint", "the issuer is the portal origin, not C2's internal API host")
	} else {
		doc, _ := provider.Discovery(checkCtx)
		log.Info("C2 discovery resolved",
			"issuer", doc.Issuer,
			"authorization_endpoint", doc.AuthorizationEndpoint,
			"token_endpoint", doc.TokenEndpoint,
			"jwks_uri", doc.JWKSURI)
	}
	cancel()

	var runner *jobs.Runner
	if notificationSvc != nil {
		runner = jobs.NewRunner(db, cfg, log, catalogSvc, routingSvc, requestSvc,
			notificationSvc, webhookSvc, reportSvc, agentSvc)
		go runner.Start(ctx)
	}

	api := httpapi.New(httpapi.Deps{
		DB: db, Config: cfg, Log: log,
		OIDC: provider, Notify: notifyClient,
		Agents: agentSvc, Audit: auditSvc,
		Contacts: contactSvc, Interactions: interactionSvc,
		Catalog: catalogSvc, Routing: routingSvc, Requests: requestSvc,
		Notifications: notificationSvc, Webhooks: webhookSvc,
		Reports: reportSvc, Callout: calloutSvc, Portal: portalSvc,
		Jobs: runner, Attachments: attachments,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	log.Info("stopped cleanly")
	return nil
}

func newLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	if lvl := os.Getenv("CC_LOG_LEVEL"); lvl != "" {
		_ = level.UnmarshalText([]byte(lvl))
	}
	opts := &slog.HandlerOptions{Level: level}

	// Text in development because a human is reading it; JSON in production
	// because a log shipper is.
	if cfg.IsProd() {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func authMode(c *notify.Client) string {
	if c.UsesPrivateKey() {
		return "private_key_jwt"
	}
	return "client_secret_basic"
}
