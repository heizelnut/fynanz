package main

import "testing"

func TestNormalizePublicBase(t *testing.T) {
	cases := map[string]string{
		"":                            "http://localhost:8080",
		"http://localhost:8080":       "http://localhost:8080",
		"https://fynanz.lab.heiz.cc":  "https://fynanz.lab.heiz.cc",
		"fynanz.lab.heiz.cc":          "https://fynanz.lab.heiz.cc",
		"fynanz.lab.heiz.cc/":         "https://fynanz.lab.heiz.cc",
		"FYNANZ.LAB.HEIZ.CC":          "https://FYNANZ.LAB.HEIZ.CC",
		"localhost:8080":              "http://localhost:8080",
		"127.0.0.1:8080":              "http://127.0.0.1:8080",
		"  fynanz.lab.heiz.cc:8443  ": "https://fynanz.lab.heiz.cc:8443",
	}
	for in, want := range cases {
		if got := normalizePublicBase(in); got != want {
			t.Errorf("normalizePublicBase(%q) = %q, want %q", in, got, want)
		}
	}
}
