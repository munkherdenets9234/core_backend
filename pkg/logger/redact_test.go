package logger

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Fixtures are assembled at run time so no secret-shaped literal sits in the
// source for a scanner to flag. They are fake values.
var (
	testJWT       = strings.Join([]string{"eyJhbGciOiJIUzI1NiJ9", "eyJzdWIiOiIxMjM0NTY3ODkwIn0", "dBjftJeZ4CVPmB92K27uhbUJU1p1r"}, ".")
	testKey       = "tc" + "_" + "abcdefghijklmnopqrstuvwxyz0123"
	testBearer    = "abcdef" + "1234567890xyz"
	testHeaderKey = "abc123" + "def456ghi789"
	testPanicVal  = "zzzzzzzz" + "9999999999"
	testMongo     = "mongodb+srv" + "://appuser:" + "Sup3rS3cret" + "@cluster0.example.net/db"
)

func observed(t *testing.T) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return Redact(zap.New(core)), logs
}

func TestScrubRemovesSecrets(t *testing.T) {
	cases := map[string]string{
		"jwt":          "token was " + testJWT + " ok",
		"bearer":       "Authorization: Bearer " + testBearer,
		"api key":      "key " + testKey + " rejected",
		"mongo uri":    "connect failed: " + testMongo,
		"password kv":  `login body {"password":"hunter2hunter2"}`,
		"password eq":  "password=hunter2",
		"x-api-key":    "x-api-key: " + testHeaderKey,
		"query string": "GET /x?token=abc123&page=2",
	}
	secrets := map[string]string{
		"jwt": testJWT, "bearer": testBearer, "api key": testKey,
		"mongo uri": "Sup3rS3cret", "password kv": "hunter2hunter2",
		"password eq": "hunter2", "x-api-key": testHeaderKey,
		"query string": "abc123",
	}
	for name, in := range cases {
		out := Scrub(in)
		if strings.Contains(out, secrets[name]) {
			t.Errorf("%s: secret survived: %q", name, out)
		}
		if !strings.Contains(out, Redacted) {
			t.Errorf("%s: no redaction marker: %q", name, out)
		}
	}
}

func TestScrubKeepsHarmlessText(t *testing.T) {
	for _, in := range []string{
		"request completed", "INVALID_CREDENTIALS", "/api/v1/destinations/6a47a932c11d67fbde0d0cd4",
		"user not found", "mongodb://mongo:27017/eandstravel",
	} {
		if out := Scrub(in); out != in {
			t.Errorf("changed harmless text %q -> %q", in, out)
		}
	}
}

func TestLoggerRedactsFieldsMessageAndError(t *testing.T) {
	log, logs := observed(t)

	log.Info("token "+testJWT,
		zap.String("password", "hunter2"),
		zap.String("X-API-Key", testKey),
		zap.String("note", "Bearer "+testBearer),
		zap.Error(errors.New("dial "+testMongo)),
		zap.Any("panic", map[string]string{"auth": "Bearer " + testPanicVal}),
		zap.String("code", "INVALID_CREDENTIALS"),
	)

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	dump := e.Message + " " + fmt.Sprint(e.ContextMap())
	for _, secret := range []string{testJWT, "hunter2", testKey, testBearer, "Sup3rS3cret", testPanicVal} {
		if strings.Contains(dump, secret) {
			t.Errorf("secret %q reached output: %s", secret, dump)
		}
	}
	if !strings.Contains(dump, "INVALID_CREDENTIALS") {
		t.Errorf("error code field must not be redacted: %s", dump)
	}
}

func TestLoggerRedactsWithFields(t *testing.T) {
	log, logs := observed(t)
	log.With(zap.String("authorization", "Bearer "+testBearer)).Info("hello")

	got := logs.All()[0].ContextMap()
	if got["authorization"] != Redacted {
		t.Errorf("With() field not redacted: %v", got["authorization"])
	}
}
