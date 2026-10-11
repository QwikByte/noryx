-- Rules can choose the entries about the servers with a tag or of a network instead of a node or
-- server. Channels send up to messages_per_minute messages a minute after the first few, with up
-- to entries_per_message entries each, and give times in time_zone, an IANA time zone or empty
-- for UTC.
ALTER TABLE notification_rules ADD COLUMN tag TEXT NOT NULL DEFAULT '';
ALTER TABLE notification_rules ADD COLUMN network_id TEXT NOT NULL DEFAULT '';
ALTER TABLE notification_channels ADD COLUMN messages_per_minute INTEGER NOT NULL DEFAULT 1;
ALTER TABLE notification_channels ADD COLUMN entries_per_message INTEGER NOT NULL DEFAULT 10;
ALTER TABLE notification_channels ADD COLUMN time_zone TEXT NOT NULL DEFAULT '';
