package config

import (
	"slices"
	"testing"
)

func TestGetenvPorts(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want []int
	}{
		{
			name: "unset falls back to the defaults",
			env:  "",
			want: defaultPorts,
		},
		{
			name: "blank falls back to the defaults",
			env:  "   ",
			want: defaultPorts,
		},
		{
			name: "single port",
			env:  "8080",
			want: []int{8080},
		},
		{
			name: "comma-separated list",
			env:  "22,80,443",
			want: []int{22, 80, 443},
		},
		{
			name: "surrounding whitespace is trimmed",
			env:  " 22 , 80 ,443 ",
			want: []int{22, 80, 443},
		},
		{
			name: "junk entries are skipped, not fatal",
			env:  "22,http,,80,70000,0,-1",
			want: []int{22, 80},
		},
		{
			name: "nothing usable falls back to the defaults",
			env:  "http,https",
			want: defaultPorts,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NETWATCH_PORTS", tt.env)

			got := getenvPorts("NETWATCH_PORTS", defaultPorts)
			if !slices.Equal(got, tt.want) {
				t.Errorf("getenvPorts(%q) = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

// The defaults are handed out as a copy: a caller assigning them to
// Scanner.Ports and sorting or truncating them must not corrupt every
// later Load.
func TestGetenvPortsDefaultsAreCopied(t *testing.T) {
	t.Setenv("NETWATCH_PORTS", "")
	original := slices.Clone(defaultPorts)

	got := getenvPorts("NETWATCH_PORTS", defaultPorts)
	got[0] = 1

	if !slices.Equal(defaultPorts, original) {
		t.Errorf("defaultPorts = %v after mutating the returned slice, want %v", defaultPorts, original)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("NETWATCH_PORTS", "")
	t.Setenv("NETWATCH_DEFAULT_TARGET", "")
	t.Setenv("NETWATCH_HTTP_ADDR", "")

	cfg := Load()

	if !slices.Equal(cfg.Ports, defaultPorts) {
		t.Errorf("Ports = %v, want %v", cfg.Ports, defaultPorts)
	}
	if cfg.DefaultTarget != "10.89.0.0/24" {
		t.Errorf("DefaultTarget = %q, want 10.89.0.0/24", cfg.DefaultTarget)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
}
