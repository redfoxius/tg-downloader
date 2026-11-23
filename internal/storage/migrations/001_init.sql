CREATE TABLE channels (
    id            INTEGER PRIMARY KEY,
    access_hash   INTEGER NOT NULL,
    title         TEXT NOT NULL,
    username      TEXT NOT NULL DEFAULT '',
    discussion_id INTEGER,                      -- linked discussion group, resolved at sync time
    noforwards    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE channel_settings (
    channel_id       INTEGER PRIMARY KEY,
    since_days       INTEGER NOT NULL DEFAULT 0, -- 0 = no relative limit
    since_unix       INTEGER NOT NULL DEFAULT 0, -- 0 = no absolute limit; the later cutoff wins
    include_comments INTEGER NOT NULL DEFAULT 1,
    dl_video         INTEGER NOT NULL DEFAULT 1,
    dl_document      INTEGER NOT NULL DEFAULT 1,
    dl_photo         INTEGER NOT NULL DEFAULT 1,
    ext_allow        TEXT    NOT NULL DEFAULT '', -- comma-separated document extensions, '' = any
    max_size_mb      INTEGER NOT NULL DEFAULT 0,  -- 0 = unlimited
    concurrency      INTEGER NOT NULL DEFAULT 3
);

CREATE TABLE users (
    id       INTEGER PRIMARY KEY,
    name     TEXT NOT NULL,
    username TEXT NOT NULL DEFAULT ''
);

CREATE TABLE posts (
    channel_id    INTEGER NOT NULL,
    id            INTEGER NOT NULL,
    date          INTEGER NOT NULL,
    text          TEXT NOT NULL DEFAULT '',
    entities      TEXT NOT NULL DEFAULT '[]',   -- JSON
    grouped_id    INTEGER,
    replies_count INTEGER NOT NULL DEFAULT 0,
    edit_date     INTEGER,
    PRIMARY KEY (channel_id, id)
);
CREATE INDEX posts_date ON posts (channel_id, date);

CREATE TABLE comments (
    channel_id  INTEGER NOT NULL,
    id          INTEGER NOT NULL,               -- message id in the discussion group
    post_id     INTEGER NOT NULL,
    author_id   INTEGER,
    date        INTEGER NOT NULL,
    text        TEXT NOT NULL DEFAULT '',
    entities    TEXT NOT NULL DEFAULT '[]',     -- JSON
    reply_to_id INTEGER,
    PRIMARY KEY (channel_id, id)
);
CREATE INDEX comments_post ON comments (channel_id, post_id);

CREATE TABLE media (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_kind  TEXT NOT NULL CHECK (owner_kind IN ('post', 'comment')),
    channel_id  INTEGER NOT NULL,
    owner_id    INTEGER NOT NULL,
    tg_media_id INTEGER NOT NULL,
    kind        TEXT NOT NULL CHECK (kind IN ('video', 'document', 'photo')),
    file_name   TEXT NOT NULL DEFAULT '',
    mime        TEXT NOT NULL DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    duration    INTEGER,
    path        TEXT,
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'failed')),
    error       TEXT,
    UNIQUE (owner_kind, channel_id, owner_id, tg_media_id)
);
CREATE INDEX media_status ON media (status);

CREATE TABLE sync_state (
    channel_id  INTEGER PRIMARY KEY,
    max_post_id INTEGER NOT NULL DEFAULT 0,
    covered_from INTEGER NOT NULL DEFAULT 0,  -- posts back to this unix time are synced; 0 = all history
    last_sync   INTEGER NOT NULL DEFAULT 0
);
