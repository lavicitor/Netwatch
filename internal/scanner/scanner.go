// Package scanner discovers live hosts and open ports on a target network.
package scanner

import (
	"context"

	"github.com/lavicitor/netwatch/internal/model"
)

// Scanner discovers hosts and their open ports on a target subnet.
type Scanner struct {
	// TODO: concurrency knobs, e.g.:
	//   MaxConcurrency int           // bounded worker pool size
	//   ProbeTimeout   time.Duration // per-connection-attempt deadline
	//   Ports          []int         // ports to probe per host
}

// New constructs a Scanner with sane defaults.
func New() *Scanner {
	return &Scanner{}
}

// Scan walks every host in the given CIDR (e.g. "10.89.0.0/24") and reports
// discovered hosts and open ports. Results are sent on resultsCh as they're
// found -- not buffered until the end -- so callers (particularly the API's
// SSE handler) can forward them to a connected GUI in real time. Scan
// returns once the whole sweep is done or ctx is cancelled.
//
// TODO: implement, roughly:
//   - expand the CIDR to individual IPs (net.ParseCIDR + increment)
//   - a bounded worker pool over (IP, port) pairs -- a buffered channel used
//     as a semaphore, or golang.org/x/sync/errgroup with SetLimit, to avoid
//     opening thousands of sockets at once
//   - per-probe context.WithTimeout(ctx, ...) so one filtered/firewalled
//     host can't stall the whole scan
//   - fan-in: collect each host's open ports before sending it on
//     resultsCh (one Host per discovered device, ports filled in)
//   - respect ctx cancellation throughout, so an HTTP client disconnecting
//     from /api/stream can stop the scan early
func (s *Scanner) Scan(ctx context.Context, cidr string, resultsCh chan<- model.Host) error {
	panic("TODO: implement concurrent scan")
}
