-- What nodes, their servers and datastores used in each hour, averaged from the minutes of usage_samples and
-- datastore_usage once the hour ended, and kept for as long as the settings say, beyond the week of the minutes. time is
-- the start of the hour, samples the number of minutes it averages. Like the steps of the charts, the players,
-- connections and the size of the data are the most of the hour; players, tps and connections are NULL if unknown.
CREATE TABLE usage_hours (
    time         INTEGER NOT NULL,
    node_id      TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id    TEXT    NOT NULL,
    samples      INTEGER NOT NULL,
    cpu_millis   REAL    NOT NULL,
    memory_bytes REAL    NOT NULL,
    net_received REAL    NOT NULL,
    net_sent     REAL    NOT NULL,
    disk_bytes   INTEGER NOT NULL,
    players      INTEGER,
    tps          REAL,
    PRIMARY KEY (node_id, server_id, time)
) WITHOUT ROWID;
CREATE INDEX usage_hours_time ON usage_hours (time);
CREATE TABLE datastore_usage_hours (
    time         INTEGER NOT NULL,
    datastore_id TEXT    NOT NULL REFERENCES datastores (id) ON DELETE CASCADE,
    samples      INTEGER NOT NULL,
    cpu_millis   REAL    NOT NULL,
    memory_bytes REAL    NOT NULL,
    connections  INTEGER,
    disk_bytes   INTEGER NOT NULL,
    PRIMARY KEY (datastore_id, time)
) WITHOUT ROWID;
CREATE INDEX datastore_usage_hours_time ON datastore_usage_hours (time)
