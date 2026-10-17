package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"Tower.Example.com:443.": "tower.example.com",
		"tower.example.com":      "tower.example.com",
		"  TOWER.example.com  ":  "tower.example.com",
		"tower.example.com.":     "tower.example.com",
		"a.com..":                "a.com",
		"localhost:3000":         "localhost",
		"::1":                    "::1",
		"[::1]:443":              "::1",
		"[::1]":                  "::1",
		"":                       "",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTenantHostsSerialiseAsEmptyArray(t *testing.T) {
	b, err := json.Marshal(Tenant{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"hosts":[]`) {
		t.Fatalf("hosts should be [] when unset, got %s", b)
	}
}

func TestTenantOnboardingFieldsJSONShape(t *testing.T) {
	b, err := json.Marshal(Tenant{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "onboarding_sent_at") || strings.Contains(string(b), "onboarding_sent_to") {
		t.Fatalf("onboarding keys should be omitted when unset, got %s", b)
	}
	at := time.Date(2026, 10, 17, 1, 2, 3, 0, time.UTC)
	b, err = json.Marshal(Tenant{Name: "x", OnboardingSentAt: &at, OnboardingSentTo: "a@b.mn"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"onboarding_sent_at"`, `"onboarding_sent_to":"a@b.mn"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing %s in %s", k, b)
		}
	}
}
