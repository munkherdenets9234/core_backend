// Package bootstrap wires every dependency and hands back a running app.
//
// It exists so main.go states the startup sequence and nothing else, and so
// tests can build the same wiring the binary uses rather than a second copy
// that drifts from it.
//
// The rule: a missing REQUIRED setting stops the process before anything is
// served; a missing OPTIONAL one disables exactly one feature and says so.
// Refusing to boot over something survivable is a worse outage than the one
// it prevents — and for THIS service the stakes are higher than usual, since
// every product asks it whether their tenants may work.
package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eandstravel/tenantcore/internal/api"
	"github.com/eandstravel/tenantcore/internal/config"
	"github.com/eandstravel/tenantcore/internal/middleware"
	"github.com/eandstravel/tenantcore/internal/repository"
	"github.com/eandstravel/tenantcore/internal/service"
	"github.com/eandstravel/tenantcore/pkg/logger"
	"github.com/eandstravel/tenantcore/pkg/token"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

type App struct {
	Config *config.Config
	Log    *zap.Logger
	Engine *gin.Engine

	mongo   *mongo.Client
	limiter *middleware.RateLimiter
}

// New wires everything from cfg. It returns an error rather than exiting so a
// test can assert on a bad configuration instead of killing the test binary;
// main.go is the one place that turns the error into a non-zero exit.
func New(ctx context.Context, cfg *config.Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	client, db, err := connectMongo(ctx, cfg)
	if err != nil {
		return nil, err
	}

	app, err := NewForDatabase(ctx, cfg, db, logger.Log)
	if err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}
	app.mongo = client
	return app, nil
}

// NewForDatabase wires against an already-open database. It is the half
// integration tests need: they bring their own disposable database and must
// exercise the same wiring the binary does.
func NewForDatabase(ctx context.Context, cfg *config.Config, db *mongo.Database, log *zap.Logger) (*App, error) {
	logFeatures(log, cfg)

	// Indexes are NOT optional. They carry the uniqueness constraints this
	// service's correctness rests on — two tenants sharing an API key hash
	// would make "which tenant is this" ambiguous on the hot path, and two
	// subscriptions for one tenant would make "what may they run" ambiguous.
	// Neither is a degraded feature; both are states the rest of the code
	// assumes cannot happen.
	if err := repository.EnsureIndexes(ctx, db); err != nil {
		return nil, fmt.Errorf("index setup: %w", err)
	}

	maker, err := token.NewMaker(cfg.TokenPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("token maker: %w", err)
	}

	tenants := repository.NewTenantRepo(db)
	platformUsers := repository.NewPlatformUserRepo(db)
	serviceClients := repository.NewServiceClientRepo(db)
	plans := repository.NewPlanRepo(db)
	subscriptions := repository.NewSubscriptionRepo(db)

	platformUserSvc := service.NewPlatformUserService(platformUsers, maker, cfg.TokenTTL)

	// Bootstrapping the first platform user is optional: a deployment that
	// already has one does not need the variables. A deployment with neither
	// is one nobody can log into — worth a loud warning, not a refusal to
	// start, because the entitlement surface still works and every product
	// still depends on it.
	if cfg.SuperadminBootstrapEnabled() {
		if err := platformUserSvc.EnsureBootstrap(ctx, cfg.SuperadminName, cfg.SuperadminEmail, cfg.SuperadminPassword); err != nil {
			log.Error("superadmin bootstrap failed — no platform user was created from the environment",
				zap.Error(err))
		}
	}

	limiter := middleware.NewRateLimiter()

	srv := api.NewServer(api.Deps{
		Config: cfg,
		Log:    log,

		Auth:        middleware.NewAuth(maker.Verifier()),
		RateLimiter: limiter,

		PublicKeyB64: maker.PublicKeyB64(),
		KeyID:        maker.KeyID(),

		Tenant:        service.NewTenantService(tenants),
		Plan:          service.NewPlanService(plans),
		Subscription:  service.NewSubscriptionService(subscriptions, plans),
		PlatformUser:  platformUserSvc,
		ServiceClient: service.NewServiceClientService(serviceClients),
		Entitlement:   service.NewEntitlementService(tenants, subscriptions, plans),
	})

	// The verifying key is logged at startup so it can be copied into a
	// product service without a database round trip or a console session.
	// It is public by construction; logging it leaks nothing.
	log.Info("token signing key loaded",
		zap.String("kid", maker.KeyID()),
		zap.String("public_key", maker.PublicKeyB64()))

	return &App{
		Config:  cfg,
		Log:     log,
		Engine:  srv.Handler(),
		limiter: limiter,
	}, nil
}

// Run serves until SIGINT/SIGTERM, then shuts down cleanly.
func (a *App) Run() error {
	srv := &http.Server{
		Addr:         ":" + a.Config.AppPort,
		Handler:      a.Engine,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		a.Log.Info("server starting",
			zap.String("port", a.Config.AppPort),
			zap.String("env", string(a.Config.AppEnv)))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		a.Log.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	a.Close(shutdownCtx)
	a.Log.Info("server stopped")
	return nil
}

func (a *App) Close(ctx context.Context) {
	if a.limiter != nil {
		a.limiter.Close()
	}
	if a.mongo != nil {
		_ = a.mongo.Disconnect(ctx)
	}
}

// logFeatures states every optional capability and its status at startup.
// A disabled feature is logged at WARN so it shows up in whatever filters out
// the routine lines.
func logFeatures(log *zap.Logger, cfg *config.Config) {
	for _, f := range cfg.Features() {
		if f.Enabled {
			log.Info("feature enabled", zap.String("feature", f.Name))
			continue
		}
		log.Warn("feature DISABLED — this deployment is running degraded",
			zap.String("feature", f.Name),
			zap.String("detail", f.Detail))
	}
}

func connectMongo(ctx context.Context, cfg *config.Config) (*mongo.Client, *mongo.Database, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.MongoConnectTimeout)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, nil, fmt.Errorf("mongo connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, nil, fmt.Errorf("mongo ping: %w", err)
	}
	logger.Log.Info("mongodb connected", zap.String("database", cfg.MongoDB))
	return client, client.Database(cfg.MongoDB), nil
}
