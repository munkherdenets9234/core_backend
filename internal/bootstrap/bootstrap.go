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
	"github.com/eandstravel/tenantcore/pkg/mailer"
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

	// stopJobs cancels the background jobs. Nil when none were started.
	stopJobs context.CancelFunc

	// passwordReset sends its mail in the background, so shutdown drains it:
	// a reset requested a moment before a restart should still arrive.
	passwordReset *service.PasswordResetService

	// promote sends its admin notice in the background too. Drained in Close,
	// which runs only after the HTTP server has stopped: Promote (the only
	// caller of the WaitGroup's Add) can no longer be running by then, so
	// Wait cannot race a late Add.
	promote *service.PromoteService
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
	passwordResets := repository.NewPasswordResetRepo(db)
	serviceClients := repository.NewServiceClientRepo(db)
	plans := repository.NewPlanRepo(db)
	subscriptions := repository.NewSubscriptionRepo(db)
	tenantDetails := repository.NewTenantDetailRepo(db)
	quotes := repository.NewQuoteRepo(db)
	tenantPlans := repository.NewTenantPlanRepo(db)
	siteContent := repository.NewSiteContentRepo(db)

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

	tenantSvc := service.NewTenantService(tenants)
	limiter := middleware.NewRateLimiter()
	mailLog := repository.NewMailLogRepo(db)
	mail := buildMailer(cfg, log, mailLog)
	passwordResetSvc := service.NewPasswordResetService(platformUsers, passwordResets, mail, log)
	promoteSvc := service.NewPromoteService(quotes, tenantSvc, platformUsers, mail, service.AppName, log)

	srv := api.NewServer(api.Deps{
		Config: cfg,
		Log:    log,

		Auth:        middleware.NewAuth(maker.Verifier()),
		RateLimiter: limiter,
		Mail:        mail,

		PublicKeyB64: maker.PublicKeyB64(),
		KeyID:        maker.KeyID(),

		Tenant:        tenantSvc,
		Plan:          service.NewPlanService(plans),
		Subscription:  service.NewSubscriptionService(subscriptions, plans),
		PlatformUser:  platformUserSvc,
		PasswordReset: passwordResetSvc,
		ServiceClient: service.NewServiceClientService(serviceClients),
		Entitlement:   service.NewEntitlementService(tenants, subscriptions, plans),
		Showcase:      service.NewShowcaseService(tenantDetails, tenants),
		Quote:         service.NewQuoteService(quotes),
		TenantPlan:    service.NewTenantPlanService(tenantPlans, plans, tenants),
		SiteContent:   service.NewSiteContentService(siteContent),
		Promote:       promoteSvc,
		MailLog:       service.NewMailLogService(mailLog),
	})

	// The verifying key is logged at startup so it can be copied into a
	// product service without a database round trip or a console session.
	// It is public by construction; logging it leaks nothing.
	log.Info("token signing key loaded",
		zap.String("kid", maker.KeyID()),
		zap.String("public_key", maker.PublicKeyB64()))

	stopJobs := startExpiryNotice(cfg, log, subscriptions, tenants, plans, mail)

	return &App{
		Config:   cfg,
		Log:      log,
		Engine:   srv.Handler(),
		limiter:  limiter,
		stopJobs: stopJobs,

		passwordReset: passwordResetSvc,
		promote:       promoteSvc,
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
	if a.stopJobs != nil {
		a.stopJobs()
	}
	if a.passwordReset != nil {
		a.passwordReset.Drain()
	}
	// After srv.Shutdown (see Run): no request can start a new notice now.
	if a.promote != nil {
		a.promote.Drain()
	}
	if a.limiter != nil {
		a.limiter.Close()
	}
	if a.mongo != nil {
		_ = a.mongo.Disconnect(ctx)
	}
}

// expiryCheckInterval is how often subscriptions are checked for an approaching
// end. The warning window is seven days, so hourly is far finer than it needs
// to be; it is hourly so that a failed send is retried promptly rather than
// the next day.
const expiryCheckInterval = time.Hour

// startExpiryNotice starts the hourly expiry check and returns the function
// that stops it, or nil when the notice is not configured.
//
// Same rule as every optional dependency: a missing setting disables the
// feature and says so, rather than stopping a platform four services depend
// on. What is different is the consequence, which is why the disabled case is
// a WARN that names it: a tenant loses write access when its subscription
// lapses whether or not anyone was warned, so "off" means "silent lapses".
func startExpiryNotice(
	cfg *config.Config,
	log *zap.Logger,
	subs *repository.SubscriptionRepo,
	tenants *repository.TenantRepo,
	plans *repository.PlanRepo,
	mail *mailer.Mailer,
) context.CancelFunc {
	if !cfg.ExpiryNoticeEnabled() {
		log.Warn("expiry notice is off — EXPIRY_NOTICE_EMAIL is not set, or mail is off; " +
			"subscriptions will lapse without warning")
		return nil
	}

	notifier := service.NewExpiryNotifier(subs, tenants, plans, mail, cfg.ExpiryNoticeEmail, log)
	ctx, cancel := context.WithCancel(context.Background())

	go service.Every(ctx, expiryCheckInterval, func(ctx context.Context) {
		sent, err := notifier.NotifyExpiring(ctx, time.Now())
		switch {
		case err != nil:
			// The next tick retries; nothing was claimed.
			log.Warn("expiry notice: check failed", zap.Error(err))
		case sent > 0:
			log.Info("expiry notice: warnings sent", zap.Int("sent", sent))
		default:
			log.Info("expiry notice: nothing to warn about")
		}
	})

	log.Info("expiry notice ready",
		zap.Duration("every", expiryCheckInterval),
		zap.Duration("warn_window", service.ExpiryWarnWindow))
	return cancel
}

// buildMailer returns the SMTP sender, or nil when no credentials are set.
//
// Same rule as every optional dependency: a missing setting disables one
// capability and says so, rather than stopping a platform that four services
// depend on. What it costs when off is worth naming precisely — password
// resets are the whole reason this exists, and "mail is off" and "resets are
// broken" are the same sentence.
func buildMailer(cfg *config.Config, log *zap.Logger, rec mailer.Recorder) *mailer.Mailer {
	if !cfg.EmailEnabled() {
		log.Warn("email is off — BREVO_API_KEY/MAIL_FROM_EMAIL are not both set (nor GMAIL_* in development); " +
			"POST /svc/notifications/email answers 503 and no password-reset mail is delivered")
		return nil
	}
	// Brevo wins whenever it is configured. Gmail is only reachable in
	// development (EmailEnabled already enforced that).
	if cfg.BrevoEnabled() {
		m := mailer.New(mailer.Config{
			APIKey:      cfg.BrevoAPIKey,
			FromAddress: cfg.MailFromEmail,
			FromName:    cfg.MailFromName,
			Recorder:    rec,
			Log:         log,
		})
		log.Info("email ready", zap.String("transport", "brevo-https"), zap.String("from", m.From()))
		return m
	}
	m := mailer.New(mailer.Config{
		Username: cfg.GmailEmail,
		Password: cfg.GmailPassword,
		FromName: cfg.MailFromName,
		Recorder: rec,
		Log:      log,
	})
	log.Warn("email ready via Gmail SMTP — development only", zap.String("from", m.From()))
	return m
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
