// Package model holds the types shared between the scanner, store, and API
// layers, so none of them need to import each other's internals.
package model

import "time"

type PortState struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Service  string `json:"service,omitempty"`
}

type Host struct {
	IP       string      `json:"ip"`
	Hostname string      `json:"hostname,omitempty"`
	Ports    []PortState `json:"ports"`
	LastSeen time.Time   `json:"last_seen"`
}

type ScanResult struct {
	Target     string    `json:"target"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Hosts      []Host    `json:"hosts"`
}
