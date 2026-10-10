CREATE TABLE IF NOT EXISTS blog (
 host      TEXT PRIMARY KEY,
 feed      TEXT NOT NULL DEFAULT '',
 lang      TEXT NOT NULL DEFAULT '',
 name      TEXT NOT NULL DEFAULT '',
 author    TEXT NOT NULL DEFAULT '',
 icp       TEXT NOT NULL DEFAULT '',
 summary   TEXT NOT NULL DEFAULT '',
 tag       TEXT NOT NULL DEFAULT '',
 friend    TEXT NOT NULL DEFAULT '',
 framework TEXT NOT NULL DEFAULT '',
 state     TEXT NOT NULL DEFAULT 'pend',
 cert      INTEGER NOT NULL DEFAULT 0,
 create_at TEXT NOT NULL DEFAULT '',
 check_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS blog_queue ON blog (check_at, create_at);
