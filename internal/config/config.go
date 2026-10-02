// Package config loads and validates every setting tenantcore reads.
//
// Two rules, the same ones the product services follow:
//
//   - A missing REQUIRED setting stops the process, and Validate reports
//     every missing key at once. Bringing up a new environment one restart
//     per missing variable is the slowest way to do it.
//   - A missing OPTIONAL setting disables one feature and nothing else, and
//     says so — at startup in the log, and for as long as the process runs
//     on /readyz.
package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Env string

const (
	EnvDevelopment Env = "development"
	EnvProduction  Env = "production"
	EnvTest        Env = "test"
)

type Config struct {
	AppEnv  Env
	AppPort string

	// Required.
	MongoURI string
	MongoDB  string

	// MongoConnectTimeout bounds the initial connect-and-ping at startup.
	//
	// It is a setting rather than a constant because the right value depends
	// entirely on where the database is. A container on the same host answers
	// in milliseconds; a hosted cluster reached over SRV has to resolve the
	// record, open TLS to three replica-set members and elect a primary
	// before the first ping returns, and on a slow link that is comfortably
	// more than ten seconds. Guessing low turns a working database into a
	// service that will not boot, with an error that reads like an outage.
	MongoConnectTimeout time.Duration

	// TokenPrivateKey is the base64 Ed25519 private key this service signs
	// with. Generate one with `make keygen`. It is the single most sensitive
	// value here: whoever holds it can mint a superadmin token that every
	// product service will accept.
	TokenPrivateKey string
	TokenTTL        time.Duration

	// Optional.
	SuperadminName     string
	SuperadminEmail    string
	SuperadminPassword string

	// PublishPublicKey serves the verifying key at /.well-known/tenantcore.
	// On by default: the key is public by construction and having services
	// fetch it beats copying a base64 blob into four env files by hand.
	PublishPublicKey bool

	RateLimitEnabled  bool
	AuthRatePerMinute int
	RateLimitBurst    int

	// SMTP, for transactional mail (Brevo). tenantcore is the only service
	// that holds mail credentials — products ask it to send, over /svc,
	// rather than each carrying its own copy of this key.
	//
	// SMTPUser is the SMTP login Brevo shows, not the account email, and
	// SMTPPassword is an SMTP key. MailFromEmail is the sender recipients
	// see; it must be verified in Brevo and is a separate address from the
	// login.
	SMTPHost      string
	SMTPPort      int
	SMTPUser      string
	SMTPPassword  string
	MailFromEmail string
	MailFromName  string

	// ExpiryNoticeEmail is where the platform operator is told that a
	// tenant's subscription is about to lapse. It is the operator's own
	// address, not the tenant's: the person who can renew a subscription is
	// the one who needs the warning. Blank turns the notice off.
	ExpiryNoticeEmail string
}

// IsDev reports whether stack traces and debug routing are appropriate.
// Anything not explicitly production counts as development, so a blank or
// misspelled APP_ENV never silently hides diagnostics.
func (c Config) IsDev() bool { return c.AppEnv != EnvProduction }

func (c Config) SuperadminBootstrapEnabled() bool {
	return c.SuperadminEmail != "" && c.SuperadminPassword != ""
}

// EmailEnabled reports whether this deployment can send mail. All three are
// required: a login with no key authenticates on every send and fails, and a
// key with no sender is refused by Brevo, which looks like an outage rather
// than a missing setting.
func (c Config) EmailEnabled() bool {
	return c.SMTPUser != "" && c.SMTPPassword != "" && c.MailFromEmail != ""
}

// ExpiryNoticeEnabled reports whether the subscription expiry warning can be
// sent. It needs both working mail and an address to send to; either one
// missing means no warning ever arrives, so the job is not started at all.
func (c Config) ExpiryNoticeEnabled() bool {
	return c.EmailEnabled() && c.ExpiryNoticeEmail != ""
}

type Feature struct {
	Name    string
	Enabled bool
	Detail  string
}

// Features is the full list of optional capabilities, in a fixed order. If a
// capability can be switched off by configuration it appears here, so "the
// feature was quietly off and nobody noticed" has one place to look.
func (c Config) Features() []Feature {
	return []Feature{
		{
			Name:    "superadmin_bootstrap",
			Enabled: c.SuperadminBootstrapEnabled(),
			Detail:  "SUPERADMIN_EMAIL/SUPERADMIN_PASSWORD are not both set — no first platform user is created",
		},
		{
			Name:    "public_key_endpoint",
			Enabled: c.PublishPublicKey,
			Detail:  "PUBLISH_PUBLIC_KEY=false — product services must be configured with TOKEN_PUBLIC_KEY by hand",
		},
		{
			Name:    "rate_limiting",
			Enabled: c.RateLimitEnabled,
			Detail:  "RATE_LIMIT_ENABLED=false — login and password changes accept unlimited requests",
		},
		{
			Name:    "email",
			Enabled: c.EmailEnabled(),
			Detail: "SMTP_USER/SMTP_PASSWORD/MAIL_FROM_EMAIL are not all set — POST /svc/notifications/email " +
				"answers 503 FEATURE_UNAVAILABLE, so password-reset mail is never delivered",
		},
		{
			Name:    "expiry_notice",
			Enabled: c.ExpiryNoticeEnabled(),
			Detail: "EXPIRY_NOTICE_EMAIL is not set, or mail is off — nobody is warned before " +
				"a tenant subscription lapses, and its writes start returning 402 unannounced",
		},
	}
}

// Validate checks every required setting and reports all failures together.
func (c Config) Validate() error {
	var problems []string

	require := func(val, name string) {
		if strings.TrimSpace(val) == "" {
			problems = append(problems, name+" is required")
		}
	}

	require(c.MongoURI, "MONGO_URI")
	require(c.MongoDB, "MONGO_DB")
	require(c.AppPort, "PORT (or APP_PORT)")

	// The key is only decoded here for its shape; token.NewMaker does the
	// real parse. Checking it at this point means a malformed key is
	// reported alongside every other configuration problem in one pass,
	// rather than being the thing that fails after everything else passed.
	if strings.TrimSpace(c.TokenPrivateKey) == "" {
		problems = append(problems, "TOKEN_PRIVATE_KEY is required — generate one with `make keygen`")
	}

	if c.TokenTTL < time.Minute {
		problems = append(problems, "TOKEN_TTL must be at least 1m")
	}
	// Tokens are verified offline by every product service, so a suspension
	// does not bite until the token expires. A very long TTL turns that from
	// a small window into a real one.
	if c.TokenTTL > 24*time.Hour {
		problems = append(problems, "TOKEN_TTL must be at most 24h — tokens are verified offline, so this is how long a suspended account keeps working")
	}

	// A connect timeout of zero or less would make the startup ping fail
	// instantly and report it as if the database were down.
	if c.MongoConnectTimeout <= 0 {
		problems = append(problems, "MONGO_CONNECT_TIMEOUT must be positive")
	}

	if c.SuperadminEmail != "" && c.SuperadminPassword != "" && len(c.SuperadminPassword) < 8 {
		problems = append(problems, "SUPERADMIN_PASSWORD must be at least 8 characters")
	}

	if len(problems) > 0 {
		return errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return nil
}

func Load() *Config {
	return &Config{
		AppEnv:  Env(getEnv("APP_ENV", string(EnvDevelopment))),
		AppPort: getEnv("PORT", getEnv("APP_PORT", "8090")),

		MongoURI:            getEnv("MONGO_URI", "mongodb://localhost:27017"),
		MongoDB:             getEnv("MONGO_DB", "tenantcore"),
		MongoConnectTimeout: getEnvDuration("MONGO_CONNECT_TIMEOUT", 30*time.Second),

		TokenPrivateKey: getEnv("TOKEN_PRIVATE_KEY", ""),
		TokenTTL:        getEnvDuration("TOKEN_TTL", time.Hour),

		SuperadminName:     getEnv("SUPERADMIN_NAME", ""),
		SuperadminEmail:    getEnv("SUPERADMIN_EMAIL", ""),
		SuperadminPassword: getEnv("SUPERADMIN_PASSWORD", ""),

		PublishPublicKey: getEnvBool("PUBLISH_PUBLIC_KEY", true),

		RateLimitEnabled:  getEnvBool("RATE_LIMIT_ENABLED", true),
		AuthRatePerMinute: getEnvInt("AUTH_RATE_PER_MINUTE", 10),
		RateLimitBurst:    getEnvInt("RATE_LIMIT_BURST", 5),

		SMTPHost:      getEnv("SMTP_HOST", ""),
		SMTPPort:      getEnvInt("SMTP_PORT", 0),
		SMTPUser:      getEnv("SMTP_USER", ""),
		SMTPPassword:  getEnv("SMTP_PASSWORD", ""),
		MailFromEmail: getEnv("MAIL_FROM_EMAIL", ""),
		MailFromName:  getEnv("MAIL_FROM_NAME", ""),

		ExpiryNoticeEmail: getEnv("EXPIRY_NOTICE_EMAIL", ""),
	}
}

// getEnv trims what it reads. A .env saved with CRLF endings yields values
// like "mongodb://...\r" that look correct in an editor and fail every
// connection with an error naming neither the file nor the character.
func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := getEnv(key, ""); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := getEnv(key, ""); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := getEnv(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
