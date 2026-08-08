CREATE TABLE IF NOT EXISTS ingests (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    project_name        TEXT NOT NULL,
    camera_id           TEXT NOT NULL DEFAULT '',
    card_number         TEXT NOT NULL DEFAULT '',
    source_identifier   TEXT NOT NULL,
    source_path         TEXT NOT NULL,
    destination_path    TEXT NOT NULL DEFAULT '',
    started_at          TEXT,
    copy_completed_at   TEXT,
    verify_completed_at TEXT,
    finished_at         TEXT,
    ejected_at          TEXT,
    formatted_at        TEXT,
    file_count          INTEGER NOT NULL DEFAULT 0,
    bytes_total         INTEGER NOT NULL DEFAULT 0,
    bytes_copied        INTEGER NOT NULL DEFAULT 0,
    status              TEXT NOT NULL,
    verify_status       TEXT NOT NULL DEFAULT '',
    error_message       TEXT NOT NULL DEFAULT '',
    detected_at         TEXT NOT NULL,
    created_at          TEXT NOT NULL
);

-- One row per physical mock/source folder ever seen, so restarting the
-- detector never creates a duplicate job for the same card.
CREATE UNIQUE INDEX IF NOT EXISTS idx_ingests_source_identifier
    ON ingests (source_identifier);

CREATE INDEX IF NOT EXISTS idx_ingests_project_camera
    ON ingests (project_name, camera_id);
