package main

import (
	"testing"
)

func TestDefaultVSTag(t *testing.T) {
	tests := []struct {
		phpVersion string
		want       string
	}{
		{"8.5", "vs17"},
		{"8.4", "vs17"},
		{"8.3", "vs16"},
		{"8.0", "vs16"},
		{"7.4", "vc15"},
		{"8.10", "vs17"},
	}

	for _, tt := range tests {
		if got := defaultVSTag(tt.phpVersion); got != tt.want {
			t.Errorf("defaultVSTag(%q) = %q, want %q", tt.phpVersion, got, tt.want)
		}
	}
}

func TestResolveVSTagPrefersExplicit(t *testing.T) {
	if got := resolveVSTag("vc15", "8.4"); got != "vc15" {
		t.Errorf("explicit tag not honoured: got %q", got)
	}
	if got := resolveVSTag("", "8.3"); got != "vs16" {
		t.Errorf("derived tag = %q, want vs16", got)
	}
}
