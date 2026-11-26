CREATE TABLE IF NOT EXISTS host_keys (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  hash       TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  revoked_at TEXT
);

CREATE TABLE IF NOT EXISTS rooms (
  id          TEXT PRIMARY KEY,
  topic       TEXT NOT NULL,
  turn_budget INTEGER NOT NULL,
  host_key    INTEGER REFERENCES host_keys(id),
  invite_hash TEXT,              -- NULL once the invitation is used
  created_at  TEXT NOT NULL,
  active_at   TEXT NOT NULL,
  closed_at   TEXT
);

CREATE TABLE IF NOT EXISTS tokens (
  hash    TEXT PRIMARY KEY,
  room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  side    TEXT NOT NULL,
  actor   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS tokens_room ON tokens(room_id);

CREATE TABLE IF NOT EXISTS messages (
  room_id    TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  side       TEXT NOT NULL,
  actor      TEXT NOT NULL,
  kind       TEXT NOT NULL,
  re         INTEGER NOT NULL DEFAULT 0,
  resolution INTEGER NOT NULL DEFAULT 0,
  body       TEXT NOT NULL,
  at         TEXT NOT NULL,
  PRIMARY KEY (room_id, seq)
);
