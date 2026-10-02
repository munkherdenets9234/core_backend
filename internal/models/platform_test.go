package models

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"Tower.Example.com:443.": "tower.example.com",
		"tower.example.com":      "tower.example.com",
		"  TOWER.example.com  ":  "tower.example.com",
		"tower.example.com.":     "tower.example.com",
		"localhost:3000":         "localhost",
		"":                       "",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}
