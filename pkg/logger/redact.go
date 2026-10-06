package logger

import (
	"fmt"
	"regexp"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Redacted replaces any secret found in a log entry.
const Redacted = "[REDACTED]"

// sensitiveKeys are field names whose value is never logged, whatever it
// holds. Matching is by substring on the lower-cased key, so "x-api-key",
// "user_password" and "resetToken" are all covered. A bare "code" is not
// listed: it is also the name of an error code, which is safe and useful.
var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie",
	"api_key", "apikey", "api-key", "otp", "passcode", "reset_code",
	"credential", "private_key",
}

// secretPatterns find secrets inside free text: messages, error strings,
// stack traces and stringified values. Each match is replaced whole, or only
// its value part when the pattern has a capture group named "v".
var secretPatterns = []*regexp.Regexp{
	// JSON Web Token: three base64url parts, the first starting "eyJ".
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`),
	// Authorization header value.
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+(?P<v>[A-Za-z0-9._~+/=-]{8,})`),
	// Credentials inside a connection URI: scheme://user:pass@host.
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:/@]+:(?P<v>[^\s@/]+)@`),
	// key=value, key: value and "key":"value" for sensitive key names.
	regexp.MustCompile(`(?i)["']?(?:password|passwd|secret|token|api[_-]?key|x-api-key|authorization|otp|passcode)["']?\s*[:=]\s*["']?(?P<v>[^\s"',;&}]+)`),
	// Long random strings that look like generated keys (tc_, ds_, sk_, pk_).
	regexp.MustCompile(`\b(?:tc|ds|sk|pk)_[A-Za-z0-9]{20,}\b`),
}

// Scrub returns s with every recognised secret replaced by Redacted.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	for _, re := range secretPatterns {
		s = redactMatches(re, s)
	}
	return s
}

func redactMatches(re *regexp.Regexp, s string) string {
	vi := re.SubexpIndex("v")
	if vi < 0 {
		return re.ReplaceAllString(s, Redacted)
	}
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		start, end := m[2*vi], m[2*vi+1]
		if start < 0 {
			continue
		}
		b.WriteString(s[last:start])
		b.WriteString(Redacted)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

func scrubField(f zapcore.Field) zapcore.Field {
	if isSensitiveKey(f.Key) {
		return zap.String(f.Key, Redacted)
	}
	switch f.Type {
	case zapcore.StringType:
		if out := Scrub(f.String); out != f.String {
			return zap.String(f.Key, out)
		}
	case zapcore.ErrorType:
		if err, ok := f.Interface.(error); ok {
			if out := Scrub(err.Error()); out != err.Error() {
				return zap.String(f.Key, out)
			}
		}
	case zapcore.StringerType, zapcore.ReflectType:
		// Arbitrary values (a recovered panic, a request struct): inspect
		// their printed form and keep the original only when it is clean.
		text := fmt.Sprintf("%+v", f.Interface)
		if out := Scrub(text); out != text {
			return zap.String(f.Key, out)
		}
	}
	return f
}

func scrubFields(fields []zapcore.Field) []zapcore.Field {
	if len(fields) == 0 {
		return fields
	}
	out := make([]zapcore.Field, len(fields))
	for i, f := range fields {
		out[i] = scrubField(f)
	}
	return out
}

type redactCore struct{ zapcore.Core }

func (c redactCore) With(fields []zapcore.Field) zapcore.Core {
	return redactCore{c.Core.With(scrubFields(fields))}
}

// Check is overridden so the entry is routed through this core. Without it
// the embedded core would add itself and skip the redaction in Write.
func (c redactCore) Check(ent zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(ent.Level) {
		return ce.AddCore(ent, c)
	}
	return ce
}

func (c redactCore) Write(ent zapcore.Entry, fields []zapcore.Field) error {
	ent.Message = Scrub(ent.Message)
	ent.Stack = Scrub(ent.Stack)
	return c.Core.Write(ent, scrubFields(fields))
}

// Redact wraps a logger so no secret reaches its output. Init applies it to
// the process logger; apply it to any other logger that is built by hand.
func Redact(l *zap.Logger) *zap.Logger {
	return l.WithOptions(zap.WrapCore(func(c zapcore.Core) zapcore.Core {
		return redactCore{c}
	}))
}
