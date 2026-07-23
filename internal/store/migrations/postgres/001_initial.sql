CREATE TABLE settings (
    setting_key VARCHAR(191) PRIMARY KEY,
    setting_value TEXT NOT NULL,
    updated_at VARCHAR(35) NOT NULL
);
-- gosend:split
CREATE TABLE trusted_devices (
    fingerprint VARCHAR(128) PRIMARY KEY,
    alias VARCHAR(255) NOT NULL,
    device_model VARCHAR(255) NOT NULL,
    device_type VARCHAR(32) NOT NULL,
    created_at VARCHAR(35) NOT NULL,
    updated_at VARCHAR(35) NOT NULL
);
-- gosend:split
CREATE TABLE transfer_sessions (
    id VARCHAR(64) PRIMARY KEY,
    direction VARCHAR(16) NOT NULL,
    peer_fingerprint VARCHAR(128) NOT NULL,
    peer_alias VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL,
    error_message TEXT NOT NULL,
    created_at VARCHAR(35) NOT NULL,
    updated_at VARCHAR(35) NOT NULL,
    completed_at VARCHAR(35) NULL
);
-- gosend:split
CREATE INDEX transfer_sessions_created_at_idx ON transfer_sessions (created_at);
-- gosend:split
CREATE TABLE transfer_files (
    id VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL REFERENCES transfer_sessions(id) ON DELETE CASCADE,
    file_name VARCHAR(1024) NOT NULL,
    file_size BIGINT NOT NULL,
    mime_type VARCHAR(255) NOT NULL,
    sha256 VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    bytes_transferred BIGINT NOT NULL,
    error_message TEXT NOT NULL,
    created_at VARCHAR(35) NOT NULL,
    updated_at VARCHAR(35) NOT NULL
);
-- gosend:split
CREATE INDEX transfer_files_session_id_idx ON transfer_files (session_id);
