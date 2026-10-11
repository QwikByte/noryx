-- Parameters of the engine of a datastore, each 0 for its default: the most connections at
-- once, and the MiB of InnoDB's buffer pool or PostgreSQL's shared_buffers.
ALTER TABLE datastores ADD COLUMN max_connections INTEGER NOT NULL DEFAULT 0;
ALTER TABLE datastores ADD COLUMN buffer_mb INTEGER NOT NULL DEFAULT 0;
