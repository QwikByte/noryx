-- A secret of a task that its kind keeps apart from its settings, which the API never returns:
-- the passphrase that encrypts the archives of the backup job of the master; empty for none.
ALTER TABLE tasks ADD COLUMN secret TEXT NOT NULL DEFAULT '';

-- The master has one backup job at most.
CREATE UNIQUE INDEX tasks_master_backup ON tasks (kind) WHERE kind = 'master-backup';

-- Archives of the master's database and certificate authority that its backup job keeps in a
-- storage or on a node (node_id, in a storage location of it), by the name of their file. The
-- job deletes the oldest there beyond those it keeps. They don't belong to the job, so deleting
-- it keeps them; they go with their storage or node, which keeps their files.
CREATE TABLE master_backups (
    name       TEXT    PRIMARY KEY,
    storage_id TEXT    REFERENCES backup_storages (id) ON DELETE CASCADE,
    node_id    TEXT    REFERENCES nodes (id) ON DELETE CASCADE,
    location   TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    CHECK ((storage_id IS NOT NULL) + (node_id IS NOT NULL) = 1)
);
CREATE INDEX master_backups_storage ON master_backups (storage_id);
CREATE INDEX master_backups_node ON master_backups (node_id)
