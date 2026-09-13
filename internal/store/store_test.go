// These tests run against a real Postgres -- there's no in-memory stand-in
// for the SQL they exercise. They skip (not fail) when no test database is
// configured or reachable, so `go test ./...` stays green on a machine
// without one.
//
// Point them at a database with either:
//
//	NETWATCH_TEST_DSN=postgres://user:password@localhost:5432/netwatch_test
//
// or the same five variables the app itself uses, with a TEST_ infix:
//
//	NETWATCH_TEST_DB_HOST, NETWATCH_TEST_DB_PORT (default 5432),
//	NETWATCH_TEST_DB_USER, NETWATCH_TEST_DB_PASSWORD, NETWATCH_TEST_DB_NAME
//
// Use a throwaway database: Open applies the migrations, and every test
// below truncates all four tables before it runs. Connections are made
// with sslmode=disable, the same as Open does in production.
package store

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/lavicitor/netwatch/internal/config"
	"github.com/lavicitor/netwatch/internal/model"
)

// testStore opens a store against the configured test database and hands
// back an empty one, or skips the test if there isn't a usable database.
func testStore(t *testing.T) *Store {
	t.Helper()

	cfg, ok := testDBConfig(t)
	if !ok {
		t.Skip("no test database configured: set NETWATCH_TEST_DSN or the NETWATCH_TEST_DB_* variables (see the comment at the top of store_test.go)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	s, err := Open(ctx, cfg)
	if err != nil {
		t.Skipf("test database configured but not usable, skipping: %v", err)
	}
	t.Cleanup(s.Close)

	// Each test starts from an empty schema; RESTART IDENTITY keeps ids
	// from drifting between runs, which makes failures easier to read.
	if _, err := s.pool.Exec(ctx, `TRUNCATE scan_events, scans, host_ports, hosts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate test database: %v", err)
	}
	return s
}

// testDBConfig reads the test database's location from the environment,
// preferring a full DSN if one is set.
func testDBConfig(t *testing.T) (config.DBConfig, bool) {
	t.Helper()

	if dsn := os.Getenv("NETWATCH_TEST_DSN"); dsn != "" {
		// Open builds its own DSN from the parts, so split this one up
		// rather than passing it through. Anything else in the DSN (TLS
		// settings, search_path, ...) is dropped along the way.
		parsed, err := pgconn.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("NETWATCH_TEST_DSN is set but unparseable: %v", err)
		}
		return config.DBConfig{
			Host:     parsed.Host,
			Port:     strconv.Itoa(int(parsed.Port)),
			User:     parsed.User,
			Password: parsed.Password,
			Name:     parsed.Database,
		}, true
	}

	cfg := config.DBConfig{
		Host:     os.Getenv("NETWATCH_TEST_DB_HOST"),
		Port:     os.Getenv("NETWATCH_TEST_DB_PORT"),
		User:     os.Getenv("NETWATCH_TEST_DB_USER"),
		Password: os.Getenv("NETWATCH_TEST_DB_PASSWORD"),
		Name:     os.Getenv("NETWATCH_TEST_DB_NAME"),
	}
	if cfg.Port == "" {
		cfg.Port = "5432"
	}
	return cfg, cfg.Configured()
}

func TestUpsertHost(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	firstSeen := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	id, isNew, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", LastSeen: firstSeen})
	if err != nil {
		t.Fatalf("first UpsertHost: %v", err)
	}
	if !isNew {
		t.Error("first sighting of a host reported isNew = false, want true")
	}

	// A second sighting: same row, later last_seen, and a hostname this
	// scan managed to resolve.
	seenAgain := firstSeen.Add(time.Hour).Truncate(time.Millisecond)
	sameID, isNew, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", Hostname: "printer.local", LastSeen: seenAgain})
	if err != nil {
		t.Fatalf("second UpsertHost: %v", err)
	}
	if isNew {
		t.Error("second sighting of a host reported isNew = true, want false")
	}
	if sameID != id {
		t.Errorf("host id = %d on the second sighting, want %d (same row)", sameID, id)
	}

	hostname, storedFirstSeen, lastSeen := readHost(t, s, id)
	if hostname != "printer.local" {
		t.Errorf("hostname = %q, want printer.local", hostname)
	}
	if !storedFirstSeen.Equal(firstSeen) {
		t.Errorf("first_seen = %v, want %v (it must not move on later sightings)", storedFirstSeen, firstSeen)
	}
	if !lastSeen.Equal(seenAgain) {
		t.Errorf("last_seen = %v, want %v", lastSeen, seenAgain)
	}

	// A scan that resolved no hostname must not erase the one on record.
	if _, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", LastSeen: seenAgain.Add(time.Minute)}); err != nil {
		t.Fatalf("third UpsertHost: %v", err)
	}
	if hostname, _, _ := readHost(t, s, id); hostname != "printer.local" {
		t.Errorf("hostname = %q after a scan with no hostname, want printer.local", hostname)
	}

	// A different address is a different host.
	otherID, isNew, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.11", LastSeen: seenAgain})
	if err != nil {
		t.Fatalf("UpsertHost for a second IP: %v", err)
	}
	if !isNew || otherID == id {
		t.Errorf("second IP: id = %d, isNew = %v; want a new id (not %d) and isNew = true", otherID, isNew, id)
	}
}

func TestUpsertHostDefaultsLastSeen(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// The API always fills LastSeen in, but a zero value must not end up
	// in the column as year 1.
	before := time.Now().Add(-time.Minute)
	id, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.20"})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	if _, _, lastSeen := readHost(t, s, id); lastSeen.Before(before) {
		t.Errorf("last_seen = %v, want roughly now", lastSeen)
	}
}

func TestUpsertPorts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	hostID, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", LastSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	// First scan: everything it found is newly open.
	opened, closed, err := s.UpsertPorts(ctx, hostID, []model.PortState{
		{Port: 22, Protocol: "tcp"},
		{Port: 80, Protocol: "tcp", Service: "http"},
	})
	if err != nil {
		t.Fatalf("first UpsertPorts: %v", err)
	}
	if got := portNumbers(opened); !slices.Equal(got, []int{22, 80}) {
		t.Errorf("opened = %v, want [22 80]", got)
	}
	if len(closed) != 0 {
		t.Errorf("closed = %v, want none", portNumbers(closed))
	}

	// Same result again: no change either way.
	opened, closed, err = s.UpsertPorts(ctx, hostID, []model.PortState{
		{Port: 22, Protocol: "tcp"},
		{Port: 80, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatalf("second UpsertPorts: %v", err)
	}
	if len(opened) != 0 || len(closed) != 0 {
		t.Errorf("unchanged scan reported opened = %v, closed = %v; want neither", portNumbers(opened), portNumbers(closed))
	}
	if got := readPorts(t, s, hostID); !slices.Equal(portNumbers(got), []int{22, 80}) {
		t.Errorf("stored ports = %v, want [22 80]", portNumbers(got))
	}

	// 443 appeared, 22 went away.
	opened, closed, err = s.UpsertPorts(ctx, hostID, []model.PortState{
		{Port: 80, Protocol: "tcp"},
		{Port: 443, Protocol: "tcp"},
	})
	if err != nil {
		t.Fatalf("third UpsertPorts: %v", err)
	}
	if got := portNumbers(opened); !slices.Equal(got, []int{443}) {
		t.Errorf("opened = %v, want [443]", got)
	}
	if got := portNumbers(closed); !slices.Equal(got, []int{22}) {
		t.Errorf("closed = %v, want [22]", got)
	}
	if got := readPorts(t, s, hostID); !slices.Equal(portNumbers(got), []int{80, 443}) {
		t.Errorf("stored ports = %v, want [80 443]", portNumbers(got))
	}

	// A host that answers with nothing open closes everything it had.
	opened, closed, err = s.UpsertPorts(ctx, hostID, nil)
	if err != nil {
		t.Fatalf("fourth UpsertPorts: %v", err)
	}
	if len(opened) != 0 {
		t.Errorf("opened = %v, want none", portNumbers(opened))
	}
	if got := portNumbers(closed); !slices.Equal(got, []int{80, 443}) {
		t.Errorf("closed = %v, want [80 443]", got)
	}
	if got := readPorts(t, s, hostID); len(got) != 0 {
		t.Errorf("stored ports = %v, want none", portNumbers(got))
	}
}

// Ports are keyed by (port, protocol), so an empty protocol from the
// scanner has to land on the same row as an explicit "tcp" -- otherwise
// every scan would report the same port as both opened and closed.
func TestUpsertPortsDefaultsProtocol(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	hostID, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", LastSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	opened, _, err := s.UpsertPorts(ctx, hostID, []model.PortState{{Port: 22}})
	if err != nil {
		t.Fatalf("first UpsertPorts: %v", err)
	}
	if len(opened) != 1 || opened[0].Protocol != "tcp" {
		t.Fatalf("opened = %+v, want one port with protocol tcp", opened)
	}
	if stored := readPorts(t, s, hostID); len(stored) != 1 || stored[0].Protocol != "tcp" {
		t.Fatalf("stored ports = %+v, want one tcp row", stored)
	}

	opened, closed, err := s.UpsertPorts(ctx, hostID, []model.PortState{{Port: 22, Protocol: "tcp"}})
	if err != nil {
		t.Fatalf("second UpsertPorts: %v", err)
	}
	if len(opened) != 0 || len(closed) != 0 {
		t.Errorf("opened = %+v, closed = %+v; want neither (same port, spelled out)", opened, closed)
	}
}

func TestBeginAndFinishScan(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	scanID, err := s.BeginScan(ctx, "10.89.0.0/24")
	if err != nil {
		t.Fatalf("BeginScan: %v", err)
	}
	if scanID == 0 {
		t.Fatal("BeginScan returned id 0")
	}

	target, finishedAt, hostCount, openPortCount := readScan(t, s, scanID)
	if target != "10.89.0.0/24" {
		t.Errorf("target = %q, want 10.89.0.0/24", target)
	}
	if finishedAt != nil {
		t.Errorf("finished_at = %v on a running scan, want NULL", finishedAt)
	}
	if hostCount != 0 || openPortCount != 0 {
		t.Errorf("counts before finishing = (%d, %d), want (0, 0)", hostCount, openPortCount)
	}

	if err := s.FinishScan(ctx, scanID, 3, 7); err != nil {
		t.Fatalf("FinishScan: %v", err)
	}

	_, finishedAt, hostCount, openPortCount = readScan(t, s, scanID)
	if finishedAt == nil {
		t.Error("finished_at is still NULL after FinishScan")
	}
	if hostCount != 3 || openPortCount != 7 {
		t.Errorf("counts after finishing = (%d, %d), want (3, 7)", hostCount, openPortCount)
	}
}

func TestRecordEvent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	scanID, err := s.BeginScan(ctx, "10.89.0.0/24")
	if err != nil {
		t.Fatalf("BeginScan: %v", err)
	}
	hostID, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", LastSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	port := 22
	if err := s.RecordEvent(ctx, scanID, hostID, nil, "host_new"); err != nil {
		t.Fatalf("RecordEvent(host_new): %v", err)
	}
	if err := s.RecordEvent(ctx, scanID, hostID, &port, "port_opened"); err != nil {
		t.Fatalf("RecordEvent(port_opened): %v", err)
	}
	if err := s.RecordEvent(ctx, scanID, hostID, &port, "port_closed"); err != nil {
		t.Fatalf("RecordEvent(port_closed): %v", err)
	}

	events := readEvents(t, s, scanID)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	if events[0].eventType != "host_new" || events[0].port != nil {
		t.Errorf("first event = %+v, want host_new with no port", events[0])
	}
	if events[1].eventType != "port_opened" || events[1].port == nil || *events[1].port != 22 {
		t.Errorf("second event = %+v, want port_opened on port 22", events[1])
	}
	if events[2].eventType != "port_closed" || events[2].port == nil || *events[2].port != 22 {
		t.Errorf("third event = %+v, want port_closed on port 22", events[2])
	}
}

// The schema's CHECK constraint is the only thing rejecting a bad event
// type, so make sure the error actually comes back rather than being
// swallowed.
func TestRecordEventRejectsUnknownType(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	scanID, err := s.BeginScan(ctx, "10.89.0.0/24")
	if err != nil {
		t.Fatalf("BeginScan: %v", err)
	}
	hostID, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", LastSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	if err := s.RecordEvent(ctx, scanID, hostID, nil, "host_exploded"); err == nil {
		t.Error("RecordEvent accepted an event type the schema doesn't allow")
	}
}

func TestRecentEvents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Empty database: an empty slice, not nil -- /api/events serves this
	// straight to the GUI, which expects a JSON array.
	events, err := s.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents on an empty database: %v", err)
	}
	if events == nil || len(events) != 0 {
		t.Fatalf("events on an empty database = %+v, want an empty non-nil slice", events)
	}

	scanID, err := s.BeginScan(ctx, "10.89.0.0/24")
	if err != nil {
		t.Fatalf("BeginScan: %v", err)
	}
	firstID, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", LastSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	secondID, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.20", LastSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}

	port := 22
	if err := s.RecordEvent(ctx, scanID, firstID, nil, "host_new"); err != nil {
		t.Fatalf("RecordEvent(host_new): %v", err)
	}
	if err := s.RecordEvent(ctx, scanID, firstID, &port, "port_opened"); err != nil {
		t.Fatalf("RecordEvent(port_opened): %v", err)
	}
	if err := s.RecordEvent(ctx, scanID, secondID, &port, "port_closed"); err != nil {
		t.Fatalf("RecordEvent(port_closed): %v", err)
	}

	events, err = s.RecentEvents(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}

	// Most recent first, so the last event recorded leads.
	if events[0].EventType != "port_closed" || events[0].HostIP != "10.89.0.20" {
		t.Errorf("first event = %+v, want port_closed on 10.89.0.20", events[0])
	}
	if events[0].Port == nil || *events[0].Port != 22 {
		t.Errorf("first event port = %v, want 22", events[0].Port)
	}
	if events[1].EventType != "port_opened" || events[1].HostIP != "10.89.0.10" {
		t.Errorf("second event = %+v, want port_opened on 10.89.0.10", events[1])
	}
	if events[2].EventType != "host_new" || events[2].Port != nil {
		t.Errorf("third event = %+v, want host_new with no port", events[2])
	}
	if events[2].ScanID != scanID || events[2].HostID != firstID {
		t.Errorf("third event ids = scan %d host %d, want scan %d host %d",
			events[2].ScanID, events[2].HostID, scanID, firstID)
	}

	// limit caps the read at the newest events rather than the oldest.
	events, err = s.RecentEvents(ctx, 2)
	if err != nil {
		t.Fatalf("RecentEvents(limit 2): %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events with limit 2, want 2: %+v", len(events), events)
	}
	if events[0].EventType != "port_closed" || events[1].EventType != "port_opened" {
		t.Errorf("limited events = %+v, want the two most recent", events)
	}
}

func TestListHosts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Empty database: an empty slice, not nil -- /api/hosts serves this
	// straight to the GUI, which expects a JSON array.
	hosts, err := s.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts on an empty database: %v", err)
	}
	if hosts == nil {
		t.Error("ListHosts returned nil, want an empty slice")
	}
	if len(hosts) != 0 {
		t.Errorf("hosts = %+v, want none", hosts)
	}

	lastSeen := time.Now().Truncate(time.Millisecond)
	withPorts, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.10", Hostname: "printer.local", LastSeen: lastSeen})
	if err != nil {
		t.Fatalf("UpsertHost: %v", err)
	}
	if _, _, err := s.UpsertPorts(ctx, withPorts, []model.PortState{
		{Port: 80, Protocol: "tcp", Service: "http"},
		{Port: 22, Protocol: "tcp"},
	}); err != nil {
		t.Fatalf("UpsertPorts: %v", err)
	}

	// A host that answered but had nothing open still belongs in the list.
	if _, _, err := s.UpsertHost(ctx, model.Host{IP: "10.89.0.11", LastSeen: lastSeen}); err != nil {
		t.Fatalf("UpsertHost for the second host: %v", err)
	}

	hosts, err = s.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("got %d hosts, want 2: %+v", len(hosts), hosts)
	}

	// Ordered by address, so the /24's .10 comes before its .11.
	if hosts[0].IP != "10.89.0.10" || hosts[1].IP != "10.89.0.11" {
		t.Fatalf("hosts = %q, %q; want 10.89.0.10 then 10.89.0.11", hosts[0].IP, hosts[1].IP)
	}
	if hosts[0].Hostname != "printer.local" {
		t.Errorf("hostname = %q, want printer.local", hosts[0].Hostname)
	}
	if !hosts[0].LastSeen.Equal(lastSeen) {
		t.Errorf("last_seen = %v, want %v", hosts[0].LastSeen, lastSeen)
	}
	if got := portNumbers(hosts[0].Ports); !slices.Equal(got, []int{22, 80}) {
		t.Errorf("ports = %v, want [22 80]", got)
	}
	if hosts[0].Ports[1].Service != "http" {
		t.Errorf("service on port 80 = %q, want http", hosts[0].Ports[1].Service)
	}
	if len(hosts[1].Ports) != 0 {
		t.Errorf("second host has ports %+v, want none", hosts[1].Ports)
	}
}

func readHost(t *testing.T, s *Store, hostID int64) (hostname string, firstSeen, lastSeen time.Time) {
	t.Helper()

	err := s.pool.QueryRow(context.Background(), `
		SELECT COALESCE(hostname, ''), first_seen, last_seen FROM hosts WHERE id = $1`,
		hostID).Scan(&hostname, &firstSeen, &lastSeen)
	if err != nil {
		t.Fatalf("read host %d: %v", hostID, err)
	}
	return hostname, firstSeen, lastSeen
}

func readPorts(t *testing.T, s *Store, hostID int64) []model.PortState {
	t.Helper()

	rows, err := s.pool.Query(context.Background(), `
		SELECT port, protocol, COALESCE(service_guess, '')
		  FROM host_ports WHERE host_id = $1 ORDER BY port, protocol`, hostID)
	if err != nil {
		t.Fatalf("read ports for host %d: %v", hostID, err)
	}
	defer rows.Close()

	var ports []model.PortState
	for rows.Next() {
		var p model.PortState
		if err := rows.Scan(&p.Port, &p.Protocol, &p.Service); err != nil {
			t.Fatalf("scan port row: %v", err)
		}
		ports = append(ports, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read port rows: %v", err)
	}
	return ports
}

func readScan(t *testing.T, s *Store, scanID int64) (target string, finishedAt *time.Time, hostCount, openPortCount int) {
	t.Helper()

	err := s.pool.QueryRow(context.Background(), `
		SELECT target, finished_at, host_count, open_port_count FROM scans WHERE id = $1`,
		scanID).Scan(&target, &finishedAt, &hostCount, &openPortCount)
	if err != nil {
		t.Fatalf("read scan %d: %v", scanID, err)
	}
	return target, finishedAt, hostCount, openPortCount
}

type storedEvent struct {
	eventType string
	port      *int
}

// String keeps failure messages readable: the zero-port case is a nil
// pointer, which %+v would print as an address.
func (e storedEvent) String() string {
	if e.port == nil {
		return e.eventType + " (no port)"
	}
	return fmt.Sprintf("%s on port %d", e.eventType, *e.port)
}

func readEvents(t *testing.T, s *Store, scanID int64) []storedEvent {
	t.Helper()

	rows, err := s.pool.Query(context.Background(), `
		SELECT event_type, port FROM scan_events WHERE scan_id = $1 ORDER BY id`, scanID)
	if err != nil {
		t.Fatalf("read events for scan %d: %v", scanID, err)
	}
	defer rows.Close()

	var events []storedEvent
	for rows.Next() {
		var e storedEvent
		if err := rows.Scan(&e.eventType, &e.port); err != nil {
			t.Fatalf("scan event row: %v", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read event rows: %v", err)
	}
	return events
}

func portNumbers(ports []model.PortState) []int {
	numbers := make([]int, len(ports))
	for i, p := range ports {
		numbers[i] = p.Port
	}
	return numbers
}
