-- Settings of a node that its agent applies itself: how much output the runtime keeps of each
-- container (files of up to log_file_mb MB, of which it keeps log_files; 0 for both leaves it to
-- the runtime's configuration), the space in GiB that uploads and backups leave free, and the
-- seconds between two measurements. The master sends them whenever the agent reports others.
ALTER TABLE nodes ADD COLUMN log_file_mb INTEGER NOT NULL DEFAULT 10;
ALTER TABLE nodes ADD COLUMN log_files INTEGER NOT NULL DEFAULT 3;
ALTER TABLE nodes ADD COLUMN keep_free_gb INTEGER NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN stats_interval_seconds INTEGER NOT NULL DEFAULT 5;
