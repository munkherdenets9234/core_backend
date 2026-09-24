// Command tenantcore is the platform's tenant moderator.
//
// It owns the things every product service must agree on — who a tenant is,
// who the platform's own staff are, what each tenant has bought — and answers
// one question for the products: what is this tenant allowed to run.
//
// It does not know what any product sells. That separation is the point: a
// new product is a new module name in a plan, not a change here.
//
// main does four things and delegates the rest to internal/bootstrap: load
// the environment, start the logger, wire the app, run it. The entry point
// sits at the repository root, matching tradecore-back.
package main

import (
	"context"
	"os"

	"github.com/eandstravel/tenantcore/internal/bootstrap"
	"github.com/eandstravel/tenantcore/internal/config"
	"github.com/eandstravel/tenantcore/pkg/logger"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	_ = godotenv.Load()

	cfg := config.Load()
	logger.Init(string(cfg.AppEnv))
	defer logger.Sync()

	app, err := bootstrap.New(context.Background(), cfg)
	if err != nil {
		// Configuration problems are reported as one list rather than one
		// restart at a time (see config.Validate), so this line is usually
		// all an operator needs to fix a fresh environment.
		logger.Log.Error("startup failed", zap.Error(err))
		_ = logger.Log.Sync()
		os.Exit(1)
	}

	if err := app.Run(); err != nil {
		logger.Log.Error("server stopped unexpectedly", zap.Error(err))
		_ = logger.Log.Sync()
		os.Exit(1)
	}
}
