package models

import (
	"encoding/json"
	"strings"
	"testing"
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
