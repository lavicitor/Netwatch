// Package config loads Netwatch's runtime configuration from the
// environment. Everything is optional except sane defaults -- in
// particular, the database settings are all-or-nothing opt-in; see
// DBConfig.Configured and internal/store.Open.
package config

import "os"

type Config struct {
	DB            DBConfig
	DefaultTarget string
	HTTPAddr      string
}

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
	}
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
