-- The channels in which servers take plugins and mods besides releases: beta takes betas too,
-- alpha alphas too. Installing and updating them, on their own, on many servers and scheduled,
-- picks the newest version of their channel. Like the pins, the master keeps them by node and
-- server.
CREATE TABLE plugin_channels (
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    channel    TEXT NOT NULL CHECK (channel IN ('beta', 'alpha')),
    PRIMARY KEY (node_id, server_id, project_id)
)
