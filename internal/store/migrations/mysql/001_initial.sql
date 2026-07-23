CREATE TABLE IF NOT EXISTS settings (
    setting_key VARCHAR(191) PRIMARY KEY,
    setting_value TEXT NOT NULL,
    updated_at VARCHAR(35) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- gosend:split
CREATE TABLE IF NOT EXISTS trusted_devices (
    fingerprint VARCHAR(128) PRIMARY KEY,
    alias VARCHAR(255) NOT NULL,
    device_model VARCHAR(255) NOT NULL,
    device_type VARCHAR(32) NOT NULL,
    created_at VARCHAR(35) NOT NULL,
    updated_at VARCHAR(35) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- gosend:split
CREATE TABLE IF NOT EXISTS transfer_sessions (
    id VARCHAR(64) PRIMARY KEY,
    direction VARCHAR(16) NOT NULL,
    peer_fingerprint VARCHAR(128) NOT NULL,
    peer_alias VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL,
    error_message TEXT NOT NULL,
    created_at VARCHAR(35) NOT NULL,
    updated_at VARCHAR(35) NOT NULL,
    completed_at VARCHAR(35) NULL,
    INDEX transfer_sessions_created_at_idx (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- gosend:split
CREATE TABLE IF NOT EXISTS transfer_files (
    id VARCHAR(64) PRIMARY KEY,
    session_id VARCHAR(64) NOT NULL,
    file_name VARCHAR(1024) NOT NULL,
    file_size BIGINT NOT NULL,
    mime_type VARCHAR(255) NOT NULL,
    sha256 VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    bytes_transferred BIGINT NOT NULL,
    error_message TEXT NOT NULL,
    created_at VARCHAR(35) NOT NULL,
    updated_at VARCHAR(35) NOT NULL,
    INDEX transfer_files_session_id_idx (session_id),
    CONSTRAINT transfer_files_session_fk FOREIGN KEY (session_id)
        REFERENCES transfer_sessions(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
