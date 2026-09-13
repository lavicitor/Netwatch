// Package scanner discovers live hosts and open ports on a target network.
package scanner

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lavicitor/netwatch/internal/model"
)

// Scanner discovers hosts and their open ports on a target subnet.
type Scanner struct {
	MaxConcurrency int           // bounded worker pool size
	ProbeTimeout   time.Duration // per-connection-attempt deadline
	Ports          []int         // ports to probe per host
}

const defaultMaxConcurrency = 64

// New constructs a Scanner with sane defaults.
func New() *Scanner {
	return &Scanner{
		MaxConcurrency: defaultMaxConcurrency,
		ProbeTimeout:   500 * time.Millisecond,
	}
}

// Scan walks every host in the given CIDR (e.g. "10.89.0.0/24") and reports discovered hosts and open ports.
func (s *Scanner) Scan(ctx context.Context, cidr string, resultsCh chan<- model.Host) error {
	ips, err := expandCIDR(cidr)
	if err != nil {
		return err
	}

	maxConcurrency := s.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = defaultMaxConcurrency
	}
	semaphore := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for _, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		semaphore <- struct{}{}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			defer func() { <-semaphore }()

			open, reachable := s.probeHost(ctx, ip)
			if reachable {
				resultsCh <- model.Host{
					IP:       ip,
					Ports:    open,
					LastSeen: time.Now(),
				}
			}
		}(ip)
	}

	wg.Wait()
	close(resultsCh)
	return ctx.Err()
}

func (s *Scanner) probeHost(ctx context.Context, ip string) (open []model.PortState, reachable bool) {
	var openPorts []model.PortState
	reachable = false

	for _, port := range s.Ports {
		addr := net.JoinHostPort(ip, strconv.Itoa(port))
		result := probePort(ctx, addr, s.ProbeTimeout)
		switch {
		case result.open:
			openPorts = append(openPorts, model.PortState{
				Port:     port,
				Protocol: "tcp",
			})
			reachable = true
		case result.refused:
			reachable = true
		}
	}

	return openPorts, reachable
}

type probeResult struct {
	open    bool
	refused bool
}

func probePort(ctx context.Context, addr string, timeout time.Duration) probeResult {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", addr)
	if err == nil {
		conn.Close()
		return probeResult{open: true}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return probeResult{refused: true}
	}
	// Timeout, "no route to host", or anything else -- inconclusive.
	return probeResult{}
}

func expandCIDR(cidr string) ([]string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}

	var ips []string
	for ip := ipnet.IP.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
		ips = append(ips, ip.String())
	}
	return ips, nil
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}
