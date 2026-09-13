// Package store wraps Postgres persistence for Netwatch. It is entirely
// optional at runtime: Open is only ever called when the caller has
// already checked config.DBConfig.Configured(), so a non-nil error here
// always means "configured, but the connection or migration failed" --
// see cmd/netwatch/main.go for how that's logged and handled.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, used by goose only
	"github.com/pressly/goose/v3"

	"github.com/lavicitor/netwatch/internal/config"
	"github.com/lavicitor/netwatch/internal/model"
	"github.com/lavicitor/netwatch/migrations"
)

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and applies any pending migrations.
func Open(ctx context.Context, cfg config.DBConfig) (*Store, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name,
	)

	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(connectCtx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	if err := runMigrations(dsn); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return &Store{pool: pool}, nil
}

func runMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

// UpsertHost records that a host was seen by a scan. It returns the host's
// row id and whether this is the first time Netwatch has seen that IP --
// the caller turns the latter into a "host_new" scan event.
func (s *Store) UpsertHost(ctx context.Context, host model.Host) (hostID int64, isNew bool, err error) {
	// The scanner timestamps each result as it probes it, which is closer
	// to when the host was actually seen than the time this write lands.
	lastSeen := host.LastSeen
	if lastSeen.IsZero() {
		lastSeen = time.Now()
	}

	err = s.pool.QueryRow(ctx, `SELECT id FROM hosts WHERE ip = $1::text::inet`, host.IP).Scan(&hostID)
	switch {
	case err == nil:
		// Known host. A scan that didn't resolve a hostname must not wipe
		// one an earlier scan did resolve, hence the COALESCE.
		_, err = s.pool.Exec(ctx, `
			UPDATE hosts
			   SET last_seen = $2,
			       hostname  = COALESCE(NULLIF($3, ''), hostname)
			 WHERE id = $1`,
			hostID, lastSeen, host.Hostname)
		if err != nil {
			return 0, false, fmt.Errorf("update host %s: %w", host.IP, err)
		}
		return hostID, false, nil

	case errors.Is(err, pgx.ErrNoRows):
		// New host. ON CONFLICT covers the narrow race where an
		// overlapping scan inserts the same IP between the SELECT above
		// and this INSERT: we lose the race and report it as new anyway,
		// which is a duplicate host_new event at worst.
		err = s.pool.QueryRow(ctx, `
			INSERT INTO hosts (ip, hostname, first_seen, last_seen)
			VALUES ($1::text::inet, NULLIF($2, ''), $3, $3)
			ON CONFLICT (ip) DO UPDATE SET last_seen = EXCLUDED.last_seen
			RETURNING id`,
			host.IP, host.Hostname, lastSeen).Scan(&hostID)
		if err != nil {
			return 0, false, fmt.Errorf("insert host %s: %w", host.IP, err)
		}
		return hostID, true, nil

	default:
		return 0, false, fmt.Errorf("look up host %s: %w", host.IP, err)
	}
}

// UpsertPorts reconciles one host's stored ports with what a scan just
// found, and reports the difference so the caller can write port_opened /
// port_closed events.
//
// A port that disappeared has its host_ports row deleted rather than
// flagged: the table is the current state of the host, and scan_events is
// where the history of it opening and closing lives. Keeping closed rows
// around would mean every read of "what's open right now" had to filter
// them out, for no information scan_events doesn't already hold.
func (s *Store) UpsertPorts(ctx context.Context, hostID int64, ports []model.PortState) (opened, closed []model.PortState, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit

	stored, err := storedPorts(ctx, tx, hostID)
	if err != nil {
		return nil, nil, err
	}

	storedSet := make(map[portKey]struct{}, len(stored))
	for _, p := range stored {
		storedSet[keyOf(p)] = struct{}{}
	}

	// Walking the scan's list in order keeps the emitted events (and the
	// tests) in a predictable order; the set guards against a scan
	// reporting the same port twice.
	currentSet := make(map[portKey]struct{}, len(ports))
	for _, port := range ports {
		port.Protocol = protocolOrDefault(port.Protocol)
		key := keyOf(port)
		if _, dup := currentSet[key]; dup {
			continue
		}
		currentSet[key] = struct{}{}

		if _, known := storedSet[key]; known {
			_, err = tx.Exec(ctx, `
				UPDATE host_ports
				   SET last_seen     = now(),
				       service_guess = COALESCE(NULLIF($4, ''), service_guess)
				 WHERE host_id = $1 AND port = $2 AND protocol = $3`,
				hostID, port.Port, port.Protocol, port.Service)
			if err != nil {
				return nil, nil, fmt.Errorf("update port %d on host %d: %w", port.Port, hostID, err)
			}
			continue
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO host_ports (host_id, port, protocol, service_guess)
			VALUES ($1, $2, $3, NULLIF($4, ''))`,
			hostID, port.Port, port.Protocol, port.Service)
		if err != nil {
			return nil, nil, fmt.Errorf("insert port %d on host %d: %w", port.Port, hostID, err)
		}
		opened = append(opened, port)
	}

	for _, port := range stored {
		if _, stillOpen := currentSet[keyOf(port)]; stillOpen {
			continue
		}
		_, err = tx.Exec(ctx, `
			DELETE FROM host_ports
			 WHERE host_id = $1 AND port = $2 AND protocol = $3`,
			hostID, port.Port, port.Protocol)
		if err != nil {
			return nil, nil, fmt.Errorf("delete port %d on host %d: %w", port.Port, hostID, err)
		}
		closed = append(closed, port)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit: %w", err)
	}
	return opened, closed, nil
}

// portKey identifies a host_ports row within one host, matching the
// (port, protocol) half of that table's primary key.
type portKey struct {
	port     int
	protocol string
}

func keyOf(p model.PortState) portKey {
	return portKey{port: p.Port, protocol: protocolOrDefault(p.Protocol)}
}

// protocolOrDefault mirrors the column default: the scanner only probes
// TCP today, and some callers leave the field empty.
func protocolOrDefault(protocol string) string {
	if protocol == "" {
		return "tcp"
	}
	return protocol
}

// storedPorts reads a host's currently recorded ports, ordered so that the
// events derived from them come out in a stable order.
func storedPorts(ctx context.Context, tx pgx.Tx, hostID int64) ([]model.PortState, error) {
	rows, err := tx.Query(ctx, `
		SELECT port, protocol, COALESCE(service_guess, '')
		  FROM host_ports
		 WHERE host_id = $1
		 ORDER BY port, protocol`, hostID)
	if err != nil {
		return nil, fmt.Errorf("list ports for host %d: %w", hostID, err)
	}
	defer rows.Close()

	var ports []model.PortState
	for rows.Next() {
		var p model.PortState
		if err := rows.Scan(&p.Port, &p.Protocol, &p.Service); err != nil {
			return nil, fmt.Errorf("scan port for host %d: %w", hostID, err)
		}
		ports = append(ports, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ports for host %d: %w", hostID, err)
	}
	return ports, nil
}

// BeginScan opens a scans row for a scan that's about to start and returns
// its id; FinishScan closes it out.
func (s *Store) BeginScan(ctx context.Context, target string) (scanID int64, err error) {
	err = s.pool.QueryRow(ctx, `
		INSERT INTO scans (target) VALUES ($1) RETURNING id`, target).Scan(&scanID)
	if err != nil {
		return 0, fmt.Errorf("begin scan of %s: %w", target, err)
	}
	return scanID, nil
}

// FinishScan stamps a scan as finished and records what it turned up. A
// scans row with a NULL finished_at is therefore either still running or
// belongs to a process that died mid-scan.
func (s *Store) FinishScan(ctx context.Context, scanID int64, hostCount, openPortCount int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE scans
		   SET finished_at     = now(),
		       host_count      = $2,
		       open_port_count = $3
		 WHERE id = $1`,
		scanID, hostCount, openPortCount)
	if err != nil {
		return fmt.Errorf("finish scan %d: %w", scanID, err)
	}
	return nil
}

// RecordEvent writes one row to scan_events. port is nil for events that
// are about the host rather than one of its ports. eventType must be one
// of the values the schema's CHECK constraint allows.
//
// Only "host_new", "port_opened" and "port_closed" are produced today.
// "host_gone" is the obvious follow-up, but it can't be derived from a
// single host's scan result the way these three are: it needs a query for
// every host previously seen on the target that this scan did *not* find,
// which means knowing the target's address range at write time and running
// once at the end of a scan rather than per host.
func (s *Store) RecordEvent(ctx context.Context, scanID int64, hostID int64, port *int, eventType string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO scan_events (scan_id, host_id, port, event_type)
		VALUES ($1, $2, $3, $4)`,
		scanID, hostID, port, eventType)
	if err != nil {
		return fmt.Errorf("record %s event for host %d: %w", eventType, hostID, err)
	}
	return nil
}

// ListHosts returns every host Netwatch has ever seen, each with the ports
// currently recorded as open on it. This is the read side of /api/hosts:
// unlike the in-memory buffer it survives restarts and isn't limited to
// whatever the most recent scan happened to reach.
func (s *Store) ListHosts(ctx context.Context) ([]model.Host, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host(h.ip), COALESCE(h.hostname, ''), h.last_seen,
		       p.port, COALESCE(p.protocol, ''), COALESCE(p.service_guess, '')
		  FROM hosts h
		  LEFT JOIN host_ports p ON p.host_id = h.id
		 ORDER BY h.ip, p.port, p.protocol`)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	defer rows.Close()

	// Never nil: the GUI and the API's own tests expect a JSON array.
	hosts := make([]model.Host, 0)

	// One row per host/port pair, ordered by host, so the host a row
	// belongs to is always the last one appended -- a host with no open
	// ports comes back as a single row with a NULL port.
	for rows.Next() {
		var (
			host     model.Host
			port     *int // NULL for a host with no open ports recorded
			protocol string
			service  string
		)
		if err := rows.Scan(&host.IP, &host.Hostname, &host.LastSeen, &port, &protocol, &service); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}

		if len(hosts) == 0 || hosts[len(hosts)-1].IP != host.IP {
			hosts = append(hosts, host)
		}
		if port == nil {
			continue
		}
		current := &hosts[len(hosts)-1]
		current.Ports = append(current.Ports, model.PortState{
			Port:     *port,
			Protocol: protocolOrDefault(protocol),
			Service:  service,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read hosts: %w", err)
	}
	return hosts, nil
}

// Event is one scan_events row resolved for reading: the host_id it stores
// is joined out to the IP a reader actually wants to see. Port is nil for
// events about the host itself rather than one of its ports.
type Event struct {
	ScanID    int64     `json:"scan_id"`
	HostID    int64     `json:"host_id"`
	HostIP    string    `json:"host_ip"`
	Port      *int      `json:"port"`
	EventType string    `json:"event_type"`
	CreatedAt time.Time `json:"created_at"`
}

// RecentEvents returns the newest scan events first, at most limit of
// them. This is the read side of RecordEvent, feeding the GUI's "recent
// changes" panel -- a peek at what just changed, deliberately not a full
// browsable history.
//
// The join is an inner one: scan_events.host_id is nullable in the schema,
// but every event Netwatch writes names a host, and one that didn't would
// have no address to show.
func (s *Store) RecentEvents(ctx context.Context, limit int) ([]Event, error) {
	// Events written in quick succession can land on the same created_at,
	// so the id breaks the tie and keeps repeated reads in one order.
	rows, err := s.pool.Query(ctx, `
		SELECT e.scan_id, e.host_id, host(h.ip), e.port, e.event_type, e.created_at
		  FROM scan_events e
		  JOIN hosts h ON h.id = e.host_id
		 ORDER BY e.created_at DESC, e.id DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent events: %w", err)
	}
	defer rows.Close()

	// Never nil: the GUI and the API's own tests expect a JSON array.
	events := make([]Event, 0)

	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ScanID, &e.HostID, &e.HostIP, &e.Port, &e.EventType, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return events, nil
}
