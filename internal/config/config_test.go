package config

import (
	"strings"
	"testing"
	"time"
)

func valid() Config {
	return Config{
		AppEnv:          EnvProduction,
		AppPort:         "8090",
		MongoURI:        "mongodb://localhost:27017",
		MongoDB:         "tenantcore",
		TokenPrivateKey: "ZmFrZS1idXQtcHJlc2VudA==",
		TokenTTL:        time.Hour,
		// Load always supplies this; a hand-built Config must too, or it is
		// asserting against a configuration the binary can never produce.
		MongoConnectTimeout: 30 * time.Second,
	}
}

func TestValidateAcceptsAMinimalConfig(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("a config with only the required keys should be valid, got: %v", err)
	}
}

// Bringing up an environment one restart per missing variable is the slowest
// possible way to do it.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	err := Config{AppEnv: EnvProduction}.Validate()
	if err == nil {
		t.Fatal("an empty config should not validate")
	}
	for _, want := range []string{"MONGO_URI", "MONGO_DB", "PORT", "TOKEN_PRIVATE_KEY", "TOKEN_TTL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s; got: %v", want, err)
		}
	}
}

// An operator who has no signing key needs to be told how to make one, not
// merely that one is missing.
func TestMissingSigningKeyPointsAtTheFix(t *testing.T) {
	cfg := valid()
	cfg.TokenPrivateKey = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a blank signing key should not validate")
	}
	if !strings.Contains(err.Error(), "keygen") {
		t.Errorf("error should name the command that generates one; got: %v", err)
	}
}

// Tokens are verified offline by every product service, so a suspension does
// not take effect until the current token expires. The TTL ceiling is what
// keeps that window small, and it is a configuration mistake worth refusing
// rather than a preference.
func TestValidateBoundsTheTokenTTL(t *testing.T) {
	tooShort := valid()
	tooShort.TokenTTL = 30 * time.Second
	if err := tooShort.Validate(); err == nil {
		t.Error("a 30s TTL should not validate")
	}

	tooLong := valid()
	tooLong.TokenTTL = 72 * time.Hour
	err := tooLong.Validate()
	if err == nil {
		t.Fatal("a 72h TTL should not validate")
	}
	if !strings.Contains(err.Error(), "suspended") {
		t.Errorf("the error should say why the ceiling exists; got: %v", err)
	}

	ok := valid()
	ok.TokenTTL = 24 * time.Hour
	if err := ok.Validate(); err != nil {
		t.Errorf("24h is the documented maximum and should validate; got: %v", err)
	}
}

func TestValidateRejectsAnUnusableSuperadminPassword(t *testing.T) {
	cfg := valid()
	cfg.SuperadminEmail = "admin@example.com"
	cfg.SuperadminPassword = "short"

	if err := cfg.Validate(); err == nil {
		t.Fatal("a 5-character superadmin password should not validate")
	}
}

func TestSuperadminBootstrapNeedsBothHalves(t *testing.T) {
	cases := []struct {
		email, password string
		want            bool
	}{
		{"", "", false},
		{"admin@example.com", "", false},
		{"", "password123", false},
		{"admin@example.com", "password123", true},
	}
	for _, c := range cases {
		cfg := Config{SuperadminEmail: c.email, SuperadminPassword: c.password}
		if got := cfg.SuperadminBootstrapEnabled(); got != c.want {
			t.Errorf("email=%q password=%q: got %v, want %v", c.email, c.password, got, c.want)
		}
	}
}

func TestIsDevTreatsAnythingButProductionAsDevelopment(t *testing.T) {
	for _, env := range []Env{EnvDevelopment, EnvTest, "", "prod", "Production"} {
		if !(Config{AppEnv: env}).IsDev() {
			t.Errorf("AppEnv=%q: IsDev() = false, want true", env)
		}
	}
	if (Config{AppEnv: EnvProduction}).IsDev() {
		t.Error("AppEnv=production: IsDev() = true, want false")
	}
}

func TestFeaturesNameTheSettingToFix(t *testing.T) {
	for _, f := range valid().Features() {
		if f.Enabled {
			continue
		}
		if f.Detail == "" {
			t.Errorf("feature %q is disabled with no detail", f.Name)
			continue
		}
		if !strings.ContainsAny(f.Detail, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_") {
			t.Errorf("feature %q detail should name an env var; got %q", f.Name, f.Detail)
		}
	}
}

// A .env saved with CRLF endings yields values with a trailing \r that look
// correct in an editor and fail every connection attempt.
func TestGetEnvTrimsWhitespace(t *testing.T) {
	t.Setenv("TC_TEST_TRIM", "  mongodb://host\r")
	if got := getEnv("TC_TEST_TRIM", "fallback"); got != "mongodb://host" {
		t.Errorf("got %q, want %q", got, "mongodb://host")
	}
}

func TestGetEnvDurationFallsBackOnGarbage(t *testing.T) {
	t.Setenv("TC_TEST_TTL", "not-a-duration")
	if got := getEnvDuration("TC_TEST_TTL", time.Hour); got != time.Hour {
		t.Errorf("got %v, want the fallback", got)
	}
	t.Setenv("TC_TEST_TTL", "45m")
	if got := getEnvDuration("TC_TEST_TTL", time.Hour); got != 45*time.Minute {
		t.Errorf("got %v, want 45m", got)
	}
}

// The expiry notice is mail with an address on top of it. Either half missing
// means no warning is ever sent, so the readiness entry must go off with it.
func TestExpiryNoticeNeedsMailAndAddress(t *testing.T) {
	cases := []struct {
		name    string
		mail    bool
		address string
		want    bool
	}{
		{"mail and address", true, "ops@example.com", true},
		{"mail but no address", true, "", false},
		{"address but no mail", false, "ops@example.com", false},
		{"neither", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			if tc.mail {
				c.BrevoAPIKey, c.MailFromEmail = "xkeysib-test", "me@example.com"
			}
			c.ExpiryNoticeEmail = tc.address

			if got := c.ExpiryNoticeEnabled(); got != tc.want {
				t.Fatalf("ExpiryNoticeEnabled() = %v, want %v", got, tc.want)
			}

			var feature *Feature
			for _, f := range c.Features() {
				if f.Name == "expiry_notice" {
					f := f
					feature = &f
				}
			}
			if feature == nil {
				t.Fatal("Features() has no expiry_notice entry")
			}
			if feature.Enabled != tc.want {
				t.Fatalf("expiry_notice Enabled = %v, want %v", feature.Enabled, tc.want)
			}
			if !tc.want && !strings.Contains(feature.Detail, "EXPIRY_NOTICE_EMAIL") {
				t.Fatalf("disabled detail should name EXPIRY_NOTICE_EMAIL, got %q", feature.Detail)
			}
		})
	}
}

// Gmail is a development convenience. In production it must never apply, even
// when the variables happen to be set.
func TestGmailFallbackIsDevelopmentOnly(t *testing.T) {
	for _, tc := range []struct {
		env  Env
		want bool
	}{
		{EnvDevelopment, true},
		{EnvProduction, false},
	} {
		c := valid()
		c.AppEnv = tc.env
		c.GmailEmail, c.GmailPassword = "me@gmail.com", "app-password"
		if got := c.GmailEnabled(); got != tc.want {
			t.Fatalf("%s: GmailEnabled() = %v, want %v", tc.env, got, tc.want)
		}
		if got := c.EmailEnabled(); got != tc.want {
			t.Fatalf("%s: EmailEnabled() = %v, want %v", tc.env, got, tc.want)
		}
	}
}
