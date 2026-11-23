-- Number of comments at the time the thread was last fetched; compared with replies_count to detect new ones.
ALTER TABLE posts ADD COLUMN comments_synced INTEGER NOT NULL DEFAULT 0;
