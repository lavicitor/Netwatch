// Package config loads Netwatch's runtime configuration from the
// environment. Everything is optional except sane defaults -- in
// particular, the database settings are all-or-nothing opt-in; see
// DBConfig.Configured and internal/store.Open.
package config

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

type Config struct {
	DB            DBConfig
	DefaultTarget string
	HTTPAddr      string
	Ports         []int // TCP ports probed on every host
}

// defaultPorts is what gets probed when NETWATCH_PORTS is unset: the usual
// suspects, plus the two nonstandard ports the demo stack in
// deploy/scan-network/compose.yml listens on, so a default scan of the
// default target actually turns something up.
var defaultPorts = []int{22, 80, 443, 3306, 5432, 6379, 8080, 9999, 31337}

// DBConfig holds optional Postgres connection settings. Leave these unset
// and Netwatch runs with in-memory results only -- that's a normal,
// supported mode, not a fallback from an error.
type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
}

// Configured reports whether enough has been set to attempt a connection.
func (c DBConfig) Configured() bool {
	return c.Host != "" && c.User != "" && c.Name != ""
}

func Load() Config {
	return Config{
		DB: DBConfig{
			Host:     os.Getenv("NETWATCH_DB_HOST"),
			Port:     getenvDefault("NETWATCH_DB_PORT", "5432"),
			User:     os.Getenv("NETWATCH_DB_USER"),
			Password: os.Getenv("NETWATCH_DB_PASSWORD"),
			Name:     os.Getenv("NETWATCH_DB_NAME"),
		},
		DefaultTarget: getenvDefault("NETWATCH_DEFAULT_TARGET", "10.89.0.0/24"),
		HTTPAddr:      getenvDefault("NETWATCH_HTTP_ADDR", ":8080"),
		Ports:         getenvPorts("NETWATCH_PORTS", defaultPorts),
	}
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getenvPorts reads a comma-separated port list, e.g. "22,80,443".
// Unparseable or out-of-range entries are skipped rather than treated as a
// startup error; if nothing usable is left, the defaults apply.
func getenvPorts(key string, def []int) []int {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return slices.Clone(def)
	}

	var ports []int
	for _, field := range strings.Split(raw, ",") {
		port, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return slices.Clone(def)
	}
	return ports
}
