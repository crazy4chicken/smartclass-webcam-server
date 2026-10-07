// Command server starts the smartclass webcam server: it loads configuration,
// connects to PostgreSQL, starts the WebSocket hub and serves the HTTP API
// until it receives an interrupt signal.
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

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/auth"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/config"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/httpapi"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/redact"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/storage"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

// version is the build-time release version. The release workflow injects it
// with -ldflags "-X main.version=vX.Y.Z"; local builds keep the default.
var version = "dev"

const (
	// startupTimeout bounds database connection, migration and orphan
	// finalization at startup.
	startupTimeout = 30 * time.Second
	// shutdownTimeout is how long in-flight requests get to finish after a
	// shutdown signal.
	shutdownTimeout = 30 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// run wires the server dependencies and blocks until the process is asked to
// stop. It owns the lifetime of the database pool, the auth verifier and the
// HTTP server.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	startupCtx, cancelStartup := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelStartup()

	pool, err := store.NewPool(startupCtx, cfg.DBURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	if err := store.RunMigrations(startupCtx, pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	slog.Info("database ready")

	// Stream accumulators live only in this process, so every row a previous
	// process left active is an orphan that would block its camera.
	st := store.New(pool)
	orphans, err := st.Streams.FinalizeOrphans(startupCtx)
	if err != nil {
		return fmt.Errorf("finalize orphaned streams: %w", err)
	}
	if len(orphans) > 0 {
		ids := make([]string, 0, len(orphans))
		for _, orphan := range orphans {
			ids = append(ids, orphan.ID)
		}
		slog.Warn("finalized streams left active by a previous process", "count", len(ids), "stream_ids", ids)
	}

	// Create the authenticator. In dev mode all JWT checks are skipped
	// and every request is accepted with a synthetic subject. The token
	// source is shared with object storage so both use one refreshed
	// service credential.
	//
	// Error causes copied into a response body pass through sanitize first,
	// so no configured credential can reach a caller.
	sanitize := redact.New(cfg.Secrets()...)
	var (
		authn     *auth.Auth
		svcTokens *auth.ClientCredentialsTokenSource
	)
	if cfg.DevMode {
		authn = auth.NewDevAuth()
		slog.Warn("running in dev mode — authentication is disabled")
	} else {
		opts := []iam.Option{}
		switch {
		case cfg.TeamusersClientID != "" && cfg.TeamusersSecret != "":
			// Rotating credentials: the source refreshes service tokens
			// shortly before they expire, so the process never holds an
			// expired credential.
			svcTokens = auth.NewClientCredentialsTokenSource(cfg.TeamusersURL, cfg.TeamusersClientID, cfg.TeamusersSecret, nil)
			opts = append(opts, iam.WithTokenSource(svcTokens.TokenFunc()))
			slog.Info("teamusers service credentials configured", "client_id", cfg.TeamusersClientID)
		case cfg.TeamusersSvcToken != "":
			opts = append(opts, iam.WithServiceToken(cfg.TeamusersSvcToken))
			slog.Warn("using a static teamusers service token; it expires after ten minutes")
		default:
			slog.Warn("no teamusers service credential configured; permission checks will fail closed")
		}

		authn, err = auth.New(cfg.TeamusersURL, cfg.TeamusersAud, sanitize, opts...)
		if err != nil {
			return fmt.Errorf("create auth: %w", err)
		}
		defer func() {
			if err := authn.Close(); err != nil {
				slog.Warn("close auth", "error", err)
			}
		}()
	}

	objStore, err := newObjectStorage(cfg, svcTokens)
	if err != nil {
		return fmt.Errorf("create object storage: %w", err)
	}

	hub := ws.NewHub()
	go hub.Run()

	registry := ws.NewRegistry(cfg.WSTicketTTL)
	slog.Info("websocket ticket registry ready", "ticket_ttl", cfg.WSTicketTTL)

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: httpapi.NewRouter(authn, st, hub, registry, objStore, sanitize),
		// ReadHeaderTimeout only: WebSocket connections are long-lived and
		// would be cut short by a write timeout.
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.ListenAddr, "version", version)
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverErr <- err
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("serve http: %w", err)
		}
		return nil
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining connections", "timeout", shutdownTimeout)
	}

	// Restore default signal handling so a second signal terminates the
	// process immediately instead of waiting for the drain below.
	stop()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	slog.Info("http server stopped")
	return nil
}

// newObjectStorage picks the configured object storage backend. filehouse wins
// when a URL is configured, then S3, then a discarding fallback.
func newObjectStorage(cfg *config.Config, tokens *auth.ClientCredentialsTokenSource) (storage.ObjectStorage, error) {
	if cfg.FilehouseURL != "" {
		if tokens == nil {
			return nil, fmt.Errorf("filehouse requires WEBCAM_TEAMUSERS_CLIENT_ID and WEBCAM_TEAMUSERS_CLIENT_SECRET")
		}
		store, err := storage.NewFilehouseStorage(cfg.FilehouseURL, cfg.FilehouseBucket, tokens, nil)
		if err != nil {
			return nil, err
		}
		slog.Info("object storage ready", "backend", "filehouse", "endpoint", cfg.FilehouseURL, "bucket", cfg.FilehouseBucket)
		return store, nil
	}

	if cfg.S3AccessKey == "" {
		slog.Warn("no object storage configured, stream segments will be discarded")
		return storage.NoopStorage{}, nil
	}

	objStore, err := storage.NewS3Storage(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	if err != nil {
		return nil, err
	}
	slog.Info("object storage ready", "backend", "s3", "endpoint", cfg.S3Endpoint, "bucket", cfg.S3Bucket)
	return objStore, nil
}
