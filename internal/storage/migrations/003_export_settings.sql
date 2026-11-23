ALTER TABLE channel_settings ADD COLUMN export_comments INTEGER NOT NULL DEFAULT 1;
ALTER TABLE channel_settings ADD COLUMN export_video    INTEGER NOT NULL DEFAULT 1;
ALTER TABLE channel_settings ADD COLUMN export_document INTEGER NOT NULL DEFAULT 1;
ALTER TABLE channel_settings ADD COLUMN export_photo    INTEGER NOT NULL DEFAULT 1;
