-- +goose Up
CREATE TABLE IF NOT EXISTS hosts (
    id BIGSERIAL PRIMARY KEY,
    ip INET NOT NULL UNIQUE,
    hostname TEXT,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS host_ports (
    host_id BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    port INTEGER NOT NULL,
    protocol TEXT NOT NULL DEFAULT 'tcp',
    service_guess TEXT,
    first_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, port, protocol)
);

CREATE TABLE IF NOT EXISTS scans (
    id BIGSERIAL PRIMARY KEY,
    target TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    host_count INTEGER NOT NULL DEFAULT 0,
    open_port_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS scan_events (
    id BIGSERIAL PRIMARY KEY,
    scan_id BIGINT NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    host_id BIGINT REFERENCES hosts(id) ON DELETE CASCADE,
    port INTEGER,
    event_type TEXT NOT NULL CHECK (event_type IN ('host_new','host_gone','port_opened','port_closed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_scan_events_scan_id ON scan_events(scan_id);
CREATE INDEX IF NOT EXISTS idx_host_ports_host_id ON host_ports(host_id);

-- +goose Down
DROP TABLE IF EXISTS scan_events;
DROP TABLE IF EXISTS scans;
DROP TABLE IF EXISTS host_ports;
DROP TABLE IF EXISTS hosts;
