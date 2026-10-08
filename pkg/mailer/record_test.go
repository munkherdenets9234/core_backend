package mailer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeRecorder keeps what the mailer logged.
type fakeRecorder struct {
	mu      sync.Mutex
	entries []Entry
	err     error
	block   bool
}

func (f *fakeRecorder) Record(ctx context.Context, e Entry) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, e)
	return f.err
}

func newRecorded(t *testing.T, endpoint string, rec Recorder, log *zap.Logger) *Mailer {
	t.Helper()
	m := New(Config{
		APIKey: "test-key-not-real", FromAddress: "from@example.com",
		Endpoint: endpoint, Recorder: rec, Log: log,
	})
	if m == nil {
		t.Fatal("mailer not built")
	}
	return m
}

func relay(status int, reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
}

var codeData = map[string]string{"app": "Console", "name": "Ann", "code": "482915", "expires_in": "10 minutes"}

func TestRecorderCalledOnSuccess(t *testing.T) {
	srv := relay(http.StatusCreated, `{"messageId":"x"}`)
	defer srv.Close()
	rec := &fakeRecorder{}
	m := newRecorded(t, srv.URL, rec, nil)

	if err := m.Send(" ann@example.com ", TemplatePasswordResetCode, codeData); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(rec.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(rec.entries))
	}
	e := rec.entries[0]
	if e.Status != StatusSent || e.Error != "" || e.To != "ann@example.com" ||
		e.Template != string(TemplatePasswordResetCode) || e.Source != SourceSystem || e.TenantID != "" {
		t.Fatalf("unexpected entry %+v", e)
	}
	if time.Since(e.At) > time.Minute {
		t.Fatalf("entry time %v is not now", e.At)
	}
}

func TestRecorderCalledOnFailure(t *testing.T) {
	srv := relay(http.StatusBadRequest, `{"code":"invalid","message":"bad sender"}`)
	defer srv.Close()
	rec := &fakeRecorder{}
	m := newRecorded(t, srv.URL, rec, nil)

	if err := m.Send("ann@example.com", TemplatePasswordChanged, map[string]string{"app": "Console", "name": "Ann"}); err == nil {
		t.Fatal("want error")
	}
	if len(rec.entries) != 1 || rec.entries[0].Status != StatusFailed || rec.entries[0].Error == "" {
		t.Fatalf("unexpected entries %+v", rec.entries)
	}
}

func TestRenderFailureIsRecorded(t *testing.T) {
	rec := &fakeRecorder{}
	m := newRecorded(t, "http://127.0.0.1:1", rec, nil)
	_ = m.Send("ann@example.com", TemplatePasswordResetCode, map[string]string{"app": "x"})
	if len(rec.entries) != 1 || rec.entries[0].Status != StatusFailed {
		t.Fatalf("unexpected entries %+v", rec.entries)
	}
}

func TestSendFromCarriesServiceAndTenant(t *testing.T) {
	srv := relay(http.StatusCreated, `{}`)
	defer srv.Close()
	rec := &fakeRecorder{}
	m := newRecorded(t, srv.URL, rec, nil)

	_ = m.SendFrom(ServiceSource("carwash", "64b000000000000000000001"), "ann@example.com",
		TemplatePasswordChanged, map[string]string{"app": "Console", "name": "Ann"})
	e := rec.entries[0]
	if e.Source != "carwash" || e.TenantID != "64b000000000000000000001" {
		t.Fatalf("unexpected entry %+v", e)
	}
}

func TestErrorTextIsTruncatedAndSingleLine(t *testing.T) {
	srv := relay(http.StatusBadGateway, strings.Repeat("line one\n\tline two ", 100))
	defer srv.Close()
	rec := &fakeRecorder{}
	m := newRecorded(t, srv.URL, rec, nil)

	_ = m.Send("ann@example.com", TemplatePasswordChanged, map[string]string{"app": "Console", "name": "Ann"})
	got := rec.entries[0].Error
	if n := len([]rune(got)); n == 0 || n > maxErrorRunes {
		t.Fatalf("error length = %d, want 1..%d", n, maxErrorRunes)
	}
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("error text is not one line: %q", got)
	}
}

// The provider's reply can echo the request. Neither the code nor the API
// key may survive into the stored text, and no field holds the data.
func TestSendNeverLeaksCodeOrKeyIntoTheRow(t *testing.T) {
	srv := relay(http.StatusBadRequest, `{"message":"cannot deliver code 482915 using test-key-not-real"}`)
	defer srv.Close()
	rec := &fakeRecorder{}
	m := newRecorded(t, srv.URL, rec, nil)

	_ = m.Send("ann@example.com", TemplatePasswordResetCode, codeData)
	e := rec.entries[0]
	flat := strings.Join([]string{e.Template, e.To, e.Status, e.Error, e.Source, e.TenantID}, "|")
	for _, secret := range []string{"482915", "test-key-not-real", "Console", "10 minutes"} {
		if strings.Contains(flat, secret) {
			t.Fatalf("row leaks %q: %q", secret, flat)
		}
	}
}

func TestFailedRecordNeverFailsTheSendAndIsLogged(t *testing.T) {
	srv := relay(http.StatusCreated, `{}`)
	defer srv.Close()
	core, logs := observer.New(zap.WarnLevel)
	rec := &fakeRecorder{err: errors.New("mongo down")}
	m := newRecorded(t, srv.URL, rec, zap.New(core))

	if err := m.Send("ann@example.com", TemplatePasswordChanged, map[string]string{"app": "Console", "name": "Ann"}); err != nil {
		t.Fatalf("send must succeed when the log write fails: %v", err)
	}
	if logs.Len() != 1 {
		t.Fatalf("log lines = %d, want 1", logs.Len())
	}
	for _, f := range logs.All()[0].Context {
		if f.String == "ann@example.com" {
			t.Fatal("the recipient must not be written to the application log")
		}
	}
}

func TestSlowRecorderIsCutOff(t *testing.T) {
	srv := relay(http.StatusCreated, `{}`)
	defer srv.Close()
	m := newRecorded(t, srv.URL, &fakeRecorder{block: true}, nil)

	start := time.Now()
	if err := m.Send("ann@example.com", TemplatePasswordChanged, map[string]string{"app": "Console", "name": "Ann"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if d := time.Since(start); d > recordTimeout+2*time.Second {
		t.Fatalf("send waited %v on the recorder", d)
	}
}

func TestNoRecorderIsFine(t *testing.T) {
	srv := relay(http.StatusCreated, `{}`)
	defer srv.Close()
	m := newRecorded(t, srv.URL, nil, nil)
	if err := m.Send("ann@example.com", TemplatePasswordChanged, map[string]string{"app": "Console", "name": "Ann"}); err != nil {
		t.Fatalf("send: %v", err)
	}
}
