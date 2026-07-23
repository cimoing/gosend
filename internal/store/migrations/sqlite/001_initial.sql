CREATE TABLE settings (
    setting_key TEXT PRIMARY KEY,
    setting_value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
-- gosend:split
CREATE TABLE trusted_devices (
    fingerprint TEXT PRIMARY KEY,
    alias TEXT NOT NULL,
    device_model TEXT NOT NULL,
    device_type TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
-- gosend:split
CREATE TABLE transfer_sessions (
    id TEXT PRIMARY KEY,
    direction TEXT NOT NULL,
    peer_fingerprint TEXT NOT NULL,
    peer_alias TEXT NOT NULL,
    status TEXT NOT NULL,
    error_message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT NULL
);
-- gosend:split
CREATE INDEX transfer_sessions_created_at_idx ON transfer_sessions (created_at);
-- gosend:split
CREATE TABLE transfer_files (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    file_name TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    mime_type TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    status TEXT NOT NULL,
    bytes_transferred INTEGER NOT NULL,
    error_message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (session_id) REFERENCES transfer_sessions(id) ON DELETE CASCADE
);
-- gosend:split
CREATE INDEX transfer_files_session_id_idx ON transfer_files (session_id);
