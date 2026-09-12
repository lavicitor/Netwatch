package scanner

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lavicitor/netwatch/internal/model"
)

func TestScanner_FindOpenPort(t *testing.T) {
	// 1. Start a temporary TCP listener on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start test listener: %v", err)
	}
	defer listener.Close()

	openPort := listener.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			connection.Close()
		}
	}()

	// 2. Create a Scanner instance with the open port and a known closed port
	scanner := &Scanner{
		Ports:          []int{openPort, 9999}, // 9999 is assumed to be closed
		MaxConcurrency: 4,
		ProbeTimeout:   100 * time.Millisecond,
	}

	// 3. Run the Scan method on the localhost CIDR
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resultsCh := make(chan model.Host)
	errCh := make(chan error, 1)

	go func() {
		errCh <- scanner.Scan(ctx, "127.0.0.1/32", resultsCh)
	}()

	// 4. Collect results and verify that the open port is reported
	var got []model.Host
	for host := range resultsCh {
		got = append(got, host)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Scan returned error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("Expected 1 host, got %d", len(got))
	}

	if got[0].IP != "127.0.0.1" {
		t.Fatalf("Expected IP 127.0.0.1, got %s", got[0].IP)
	}

	if len(got[0].Ports) != 1 {
		t.Fatalf("Expected 1 open port, got %d", len(got[0].Ports))
	}

	if got[0].Ports[0].Port != openPort {
		t.Fatalf("Expected open port %d, got %d", openPort, got[0].Ports[0].Port)
	}
}
