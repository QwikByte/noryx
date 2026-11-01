-- The schema of the master's database. Later versions change it with further migrations, as the
-- master runs each migration only once. Times are Unix seconds unless a comment says otherwise.

-- Users, see the auth package. Invited users have an empty password_hash until they set a
-- password with their setup link. Disabled users can't sign in. language is the language of the
-- panel a user chose, e.g. de; empty follows the browser.
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    disabled      INTEGER NOT NULL DEFAULT 0,
    language      TEXT    NOT NULL DEFAULT ''
);

-- Sessions of the panel. Only the SHA-256 hash of a session token is stored. id names a session
-- to the panel, but unlike its token and the token's hash, it can't sign in. last_used_at, ip,
-- browser and os tell when and from where a session was used last, noted at most once a minute;
-- browser and os are names the master recognised in the User-Agent, or empty.
CREATE TABLE sessions (
    token_hash   BLOB    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at   INTEGER NOT NULL,
    id           TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    ip           TEXT    NOT NULL DEFAULT '',
    browser      TEXT    NOT NULL DEFAULT '',
    os           TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX sessions_id ON sessions (id);

-- A setup link lets a user set a password once, for new users and resets. Only the SHA-256
-- hash of its token is stored.
CREATE TABLE setup_tokens (
    token_hash BLOB    PRIMARY KEY,
    user_id    INTEGER NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);

-- Two-factor authentication: once enabled is 1, signing in also needs a code of the user's
-- authenticator app (TOTP); until then, secret belongs to a setup in progress. last_step is
-- the time step of the last code accepted, so that no code works twice. failures counts
-- wrong codes in a row, which lock the code check until locked_until.
CREATE TABLE user_mfa (
    user_id      INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret       TEXT    NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 0,
    last_step    INTEGER NOT NULL DEFAULT 0,
    failures     INTEGER NOT NULL DEFAULT 0,
    locked_until INTEGER NOT NULL DEFAULT 0
);

-- A recovery code replaces a code of the app once. Only its SHA-256 hash is stored.
CREATE TABLE recovery_codes (
    user_id   INTEGER NOT NULL REFERENCES user_mfa (user_id) ON DELETE CASCADE,
    code_hash BLOB    NOT NULL,
    PRIMARY KEY (user_id, code_hash)
);

-- API tokens let scripts use the API as their user, with all of the user's permissions or only
-- some. Only the SHA-256 hash of a token is stored; id names it to the panel and in the log, but
-- unlike the token and its hash, it can't authenticate. permissions is a JSON array of the
-- permissions it may use, or NULL for all of the user's. expires_at is 0 for tokens that don't
-- expire. last_used_at and ip tell when and from where a token was used last, noted at most once
-- a minute.
CREATE TABLE api_tokens (
    token_hash   BLOB    PRIMARY KEY,
    id           TEXT    NOT NULL UNIQUE,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    permissions  TEXT,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    ip           TEXT    NOT NULL DEFAULT '',
    UNIQUE (user_id, name)
);

-- Nodes, see the node package. join_secret_hash and join_expires_at hold the pending single-use
-- join token of a node. certificate_expires_at is when the node's certificate expires, so that the
-- panel warns about a node that stays offline until it has to be enrolled again, also after the
-- master restarted: the master stores it as it issues a certificate, and when a node presents one
-- that expires later.
--
-- Settings for the servers of a node: without a port range, any port can be used; without a
-- memory reserve, servers can get more memory than the node has. Settings that its agent applies
-- itself: how much output the runtime keeps of each container (files of up to log_file_mb MB, of
-- which it keeps log_files; 0 for both leaves it to the runtime's configuration), the space in GiB
-- that uploads and backups leave free, and the seconds between two measurements. The master sends
-- them whenever the agent reports others.
CREATE TABLE nodes (
    id                     TEXT    PRIMARY KEY,
    name                   TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    address                TEXT    NOT NULL,
    join_secret_hash       BLOB,
    join_expires_at        INTEGER,
    enrolled_at            INTEGER,
    created_at             INTEGER NOT NULL,
    default_storage        TEXT    NOT NULL DEFAULT 'default',
    port_min               INTEGER,
    port_max               INTEGER,
    memory_reserve_mb      INTEGER DEFAULT 1024,
    certificate_expires_at INTEGER,
    log_file_mb            INTEGER NOT NULL DEFAULT 10,
    log_files              INTEGER NOT NULL DEFAULT 3,
    keep_free_gb           INTEGER NOT NULL DEFAULT 1,
    stats_interval_seconds INTEGER NOT NULL DEFAULT 5
);

-- Groups give their members permissions, see the access package. permissions is a JSON
-- array. Node and server permissions apply to all servers, or only to the targets of the
-- group if all_servers is 0.
CREATE TABLE user_groups (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT    NOT NULL,
    permissions TEXT    NOT NULL,
    all_servers INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);

-- An empty server_id stands for all servers of the node, including those created later.
CREATE TABLE group_targets (
    group_id  TEXT NOT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    node_id   TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id TEXT NOT NULL,
    PRIMARY KEY (group_id, node_id, server_id)
);

CREATE TABLE group_members (
    group_id TEXT    NOT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

-- Administrators have every permission.
INSERT INTO user_groups (id, name, description, permissions, all_servers, created_at)
VALUES ('administrators', 'Administrators', 'Have every permission, also those added in later versions.', '[]', 1, unixepoch());

-- Settings of the master that administrators change in the panel, as JSON, see the
-- settings package. There is at most one row; without it the defaults apply.
CREATE TABLE settings (
    id    INTEGER PRIMARY KEY CHECK (id = 1),
    value TEXT    NOT NULL
);

-- What each user chose in the panel, see the preference package. dashboards holds the layout of
-- the overview: its widgets in their order as JSON, each with its width in columns and whether it
-- is hidden. Users without one see the panel's default layout.
CREATE TABLE dashboards (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    widgets TEXT    NOT NULL
);

-- The servers each user pinned in the panel, in the order of position. Pins go with the
-- user and with the node; the panel hides those of servers the user doesn't see.
CREATE TABLE pinned_servers (
    user_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    position  INTEGER NOT NULL,
    node_id   TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id TEXT    NOT NULL,
    PRIMARY KEY (user_id, node_id, server_id)
);

-- The other settings of the panel each user chose, as a JSON object of known keys and values,
-- e.g. {"theme":"dark","clock":"24h"}. Keys a user never set follow the browser.
CREATE TABLE user_settings (
    user_id  INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    settings TEXT    NOT NULL
);

-- Which new warnings and errors of the log pop up for each user in the panel, as a JSON object:
-- the least level, whether only those of pinned servers and of chosen nodes, servers and
-- categories, and until when none at all. Users without one get all warnings and errors.
CREATE TABLE alert_filters (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    filter  TEXT    NOT NULL
);

-- The groups of server lists that each user folded away, as a JSON list of what they are
-- grouped by and their value, e.g. "network/<id>" or "tag/lobby".
CREATE TABLE folded_groups (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    folded  TEXT    NOT NULL
);

-- The views of the servers page that each user saved, as a JSON list of their names and
-- searches. Only the user sees them.
CREATE TABLE server_views (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    views   TEXT    NOT NULL
);

-- Items of Needs attention on the overview that each user hid, as a JSON list: what each is
-- about, until when it is hidden, and how it was if it is hidden until that changes.
CREATE TABLE hidden_items (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    items   TEXT    NOT NULL
);

-- Servers live on their agents, so the master keeps what it knows about them by node and server.
-- Tags of servers, e.g. lobby or bedwars, by which the panel finds and groups them.
CREATE TABLE server_tags (
    node_id   TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id TEXT NOT NULL,
    tag       TEXT NOT NULL,
    PRIMARY KEY (node_id, server_id, tag)
);

-- Notes of servers, e.g. what a test server is for or whom to ask about it.
CREATE TABLE server_notes (
    node_id   TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id TEXT NOT NULL,
    notes     TEXT NOT NULL,
    PRIMARY KEY (node_id, server_id)
);

-- The Modrinth modpack each server was created from, so that it can move to another version
-- of the pack.
CREATE TABLE server_modpacks (
    node_id   TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id TEXT NOT NULL,
    project   TEXT NOT NULL,
    -- The ID of the version on Modrinth, its version number, and the Minecraft and loader
    -- version it needs.
    version   TEXT NOT NULL,
    number    TEXT NOT NULL,
    minecraft TEXT NOT NULL,
    loader    TEXT NOT NULL,
    -- The files the pack wrote, as a JSON object by their path in the server's data: their
    -- SHA-512 hash and, for mods, their project on Modrinth.
    files     TEXT NOT NULL,
    PRIMARY KEY (node_id, server_id)
);

-- Plugins and mods that a server keeps at their version: updating all of them, on its own or
-- on many servers, and scheduled updates leave these projects out.
CREATE TABLE plugin_pins (
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    PRIMARY KEY (node_id, server_id, project_id)
);

-- The channels in which servers take plugins and mods besides releases: beta takes betas too,
-- alpha alphas too. Installing and updating them, on their own, on many servers and scheduled,
-- picks the newest version of their channel.
CREATE TABLE plugin_channels (
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id  TEXT NOT NULL,
    project_id TEXT NOT NULL,
    channel    TEXT NOT NULL CHECK (channel IN ('beta', 'alpha')),
    PRIMARY KEY (node_id, server_id, project_id)
);

-- Templates for new servers. settings holds the settings, server.properties and plugins
-- as JSON, see the template package.
CREATE TABLE templates (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT    NOT NULL,
    settings    TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
);

-- A network is a proxy with game servers behind it, set up the way its proxy documents it.
-- forwarding_secret lets the backends verify the players the proxy forwards. proxy_type is
-- velocity or bungeecord, as a network can't ask a proxy whose node is offline.
-- forwarding is modern (Velocity's) or legacy (BungeeCord's ip_forward); firewalled records that
-- the operator confirmed that only the proxy's node reaches the backends on other nodes, which
-- legacy forwarding needs. try lists the names of the backends players join and fall back to, in
-- this order; forced_hosts the backends of host names, as [{"host": …, "servers": […]}]. Bedrock
-- players join through Geyser on the proxy at bedrock_port over UDP; 0 lets none join.
-- apply_error tells why the servers of a network were last configured in vain, until they are
-- configured again: its proxy may send players to where they no longer are.
CREATE TABLE networks (
    id                TEXT    PRIMARY KEY,
    name              TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    proxy_node_id     TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    proxy_server_id   TEXT    NOT NULL,
    proxy_type        TEXT    NOT NULL,
    forwarding_secret TEXT    NOT NULL,
    forwarding        TEXT    NOT NULL DEFAULT 'modern',
    firewalled        INTEGER NOT NULL DEFAULT 0,
    try               TEXT    NOT NULL DEFAULT '[]',
    forced_hosts      TEXT    NOT NULL DEFAULT '[]',
    bedrock_port      INTEGER NOT NULL DEFAULT 0,
    apply_error       TEXT,
    created_at        INTEGER NOT NULL,
    UNIQUE (proxy_node_id, proxy_server_id)
);

-- The game servers of a network. A server belongs to one network at most. Settings of
-- BungeeCord: only players with the permission bungeecord.server.<name> may join a restricted
-- server, and the MOTD is shown for host names that lead to it.
CREATE TABLE network_backends (
    network_id TEXT    NOT NULL REFERENCES networks (id) ON DELETE CASCADE,
    node_id    TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id  TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    position   INTEGER NOT NULL,
    restricted INTEGER NOT NULL DEFAULT 0,
    motd       TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (network_id, name),
    UNIQUE (node_id, server_id)
);

-- The private WireGuard network of the nodes: its private IPv4 range, which only changes
-- while no node is a member, and the UDP port and MTU of the nodes' interfaces.
CREATE TABLE overlay_settings (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    subnet TEXT    NOT NULL,
    port   INTEGER NOT NULL,
    mtu    INTEGER NOT NULL
);
INSERT INTO overlay_settings (id, subnet, port, mtu) VALUES (1, '10.213.0.0/24', 51820, 1420);

-- The members of the network, with their address in it and their public key; the private
-- keys stay on the nodes. endpoint is host:port at which the other nodes reach a member;
-- empty for the host of its agent's address and the network's port.
CREATE TABLE overlay_nodes (
    node_id    TEXT    PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    address    TEXT    NOT NULL UNIQUE,
    public_key TEXT    NOT NULL UNIQUE,
    endpoint   TEXT    NOT NULL DEFAULT '',
    joined_at  INTEGER NOT NULL
);

-- Datastores: MariaDB and PostgreSQL servers of networks, which the agent of a node runs.
-- port is published at the node's address in the private network of the nodes for the
-- network's servers on other nodes; 0 until one needs it. Deleting a network through the
-- API needs its datastores deleted first; removing a node forgets them with its networks.
-- max_connections and buffer_mb are parameters of the engine, each 0 for its default: the most
-- connections at once, and the MiB of InnoDB's buffer pool or PostgreSQL's shared_buffers.
CREATE TABLE datastores (
    id              TEXT    PRIMARY KEY,
    network_id      TEXT    NOT NULL REFERENCES networks (id) ON DELETE CASCADE,
    node_id         TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    engine          TEXT    NOT NULL,
    version         TEXT    NOT NULL,
    memory_mb       INTEGER NOT NULL,
    cpu_millis      INTEGER NOT NULL,
    storage         TEXT    NOT NULL,
    port            INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    max_connections INTEGER NOT NULL DEFAULT 0,
    buffer_mb       INTEGER NOT NULL DEFAULT 0,
    UNIQUE (network_id, name)
);

-- The databases of a datastore, each with a user of the same name. Their passwords reach the
-- servers through file sets; the API only returns one to those who may manage datastores.
CREATE TABLE datastore_databases (
    datastore_id TEXT    NOT NULL REFERENCES datastores (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    password     TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (datastore_id, name)
);

-- File sets: named, versioned collections of text files that the master puts on the servers
-- of tags and networks. version is the newest of the versions, of which the newest 20 are
-- kept with their files as JSON, see the fileset package. variables holds the variables with
-- their values for servers, tags, networks and all servers, as JSON; like the targets, they
-- aren't part of the versions.
CREATE TABLE file_sets (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT    NOT NULL,
    version     INTEGER NOT NULL,
    created_at  INTEGER NOT NULL,
    variables   TEXT    NOT NULL DEFAULT '[]'
);

CREATE TABLE file_set_versions (
    set_id     TEXT    NOT NULL REFERENCES file_sets (id) ON DELETE CASCADE,
    version    INTEGER NOT NULL,
    files      TEXT    NOT NULL,
    username   TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (set_id, version)
);

-- The servers a set is for: those with a tag, or the game servers or the proxy of a network.
CREATE TABLE file_set_targets (
    set_id TEXT NOT NULL REFERENCES file_sets (id) ON DELETE CASCADE,
    kind   TEXT NOT NULL,
    value  TEXT NOT NULL,
    role   TEXT NOT NULL,
    PRIMARY KEY (set_id, kind, value, role)
);

-- The secrets of a set, which only the agents fill in. The API only tells their names.
CREATE TABLE file_set_secrets (
    set_id     TEXT    NOT NULL REFERENCES file_sets (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (set_id, name)
);

-- Tasks run on a schedule: backup jobs, the panel's schedules (policies) and the backup job of
-- the master. kind tells them apart; schedule and settings are JSON, see the schedule package and
-- the kinds. saved_by is the user who saved a task last: steps that need more than the permission
-- to manage the task, e.g. backing up servers first, run with this user's permissions, and not at
-- all once the user is deleted. secret is one that its kind keeps apart from its settings, which
-- the API never returns: the passphrase that encrypts the archives of the backup job of the
-- master; empty for none.
CREATE TABLE tasks (
    id         TEXT    PRIMARY KEY,
    kind       TEXT    NOT NULL,
    name       TEXT    NOT NULL COLLATE NOCASE,
    enabled    INTEGER NOT NULL,
    schedule   TEXT    NOT NULL,
    settings   TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    saved_by   INTEGER REFERENCES users (id) ON DELETE SET NULL,
    secret     TEXT    NOT NULL DEFAULT '',
    UNIQUE (kind, name)
);

-- The master has one backup job at most.
CREATE UNIQUE INDEX tasks_master_backup ON tasks (kind) WHERE kind = 'master-backup';

-- The servers a task runs on, which follow tags and networks as they change. A target is a
-- server, all servers of a node (server_id empty), including those created later, those with a
-- tag, or those of a network: its game servers, its proxy or both (role servers, proxy or empty).
-- Targets of deleted nodes and networks go with them.
CREATE TABLE task_targets (
    task_id    TEXT NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    node_id    TEXT REFERENCES nodes (id) ON DELETE CASCADE,
    server_id  TEXT NOT NULL DEFAULT '',
    tag        TEXT NOT NULL DEFAULT '',
    network_id TEXT REFERENCES networks (id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT '',
    CHECK ((node_id IS NOT NULL) + (tag != '') + (network_id IS NOT NULL) = 1)
);
CREATE INDEX task_targets_task ON task_targets (task_id);

-- The runs of tasks, of which the newest 50 per task are kept: when they started and ended, who
-- started them by hand (empty for their schedule), their outcome (succeeded or failed), error and
-- note, and the steps of runs that take several, as JSON.
CREATE TABLE task_runs (
    id         INTEGER PRIMARY KEY,
    task_id    TEXT    NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER NOT NULL,
    started_by TEXT    NOT NULL,
    outcome    TEXT    NOT NULL,
    error      TEXT    NOT NULL,
    note       TEXT    NOT NULL,
    steps      TEXT    NOT NULL DEFAULT '[]'
);
CREATE INDEX task_runs_task ON task_runs (task_id, id);

-- S3-compatible storage that backup jobs copy their backups to, with its credentials, which
-- the API never returns.
CREATE TABLE backup_storages (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    endpoint   TEXT    NOT NULL,
    region     TEXT    NOT NULL,
    bucket     TEXT    NOT NULL,
    prefix     TEXT    NOT NULL,
    access_key TEXT    NOT NULL,
    secret_key TEXT    NOT NULL,
    path_style INTEGER NOT NULL,
    encrypt    INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

-- Copies of backups that jobs made away from the servers' nodes: in a storage or on another
-- node (copy_node, in a storage location of it). They describe the backup and the server it
-- is of as they were when it was copied, so that the server can be restored once it or its
-- node is gone; deleting the job or the server keeps them. They belong to the server on its
-- node (node_id), which they follow when it moves. Copies of a deleted storage or on a removed
-- node go with it.
CREATE TABLE backup_copies (
    id          INTEGER PRIMARY KEY,
    task_id     TEXT    REFERENCES tasks (id) ON DELETE SET NULL,
    storage_id  TEXT    REFERENCES backup_storages (id) ON DELETE CASCADE,
    copy_node   TEXT    REFERENCES nodes (id) ON DELETE CASCADE,
    location    TEXT    NOT NULL,
    server_id   TEXT    NOT NULL,
    server_name TEXT    NOT NULL,
    proxy       INTEGER NOT NULL,
    bedrock     INTEGER NOT NULL,
    node_id     TEXT    NOT NULL,
    node_name   TEXT    NOT NULL,
    backup_id   TEXT    NOT NULL,
    label       TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    size        INTEGER NOT NULL,
    paths       TEXT    NOT NULL,
    exclude     TEXT    NOT NULL,
    kept        INTEGER NOT NULL,
    copied_at   INTEGER NOT NULL,
    CHECK ((storage_id IS NOT NULL) + (copy_node IS NOT NULL) = 1)
);
CREATE UNIQUE INDEX backup_copies_storage ON backup_copies (storage_id, server_id, backup_id) WHERE storage_id IS NOT NULL;
CREATE UNIQUE INDEX backup_copies_node ON backup_copies (copy_node, server_id, backup_id) WHERE copy_node IS NOT NULL;
CREATE INDEX backup_copies_server ON backup_copies (server_id);

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
CREATE INDEX master_backups_node ON master_backups (node_id);

-- Workflows start their steps when their triggers fire. definition holds their triggers, inputs,
-- steps and options as JSON, with the values of secret headers of requests, which the API never
-- returns. hook is the SHA-256 of the token in the URL that starts a workflow, empty for none.
-- saved_by is the user who saved a workflow last, whose permissions its runs need.
CREATE TABLE workflows (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT    NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL,
    definition  TEXT    NOT NULL,
    hook        TEXT    NOT NULL DEFAULT '',
    saved_by    INTEGER REFERENCES users (id) ON DELETE SET NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- The runs of workflows, of which the newest 100 per workflow are kept: what started them, when
-- (in milliseconds), how they ended, and the steps they ran and the data of their trigger as JSON.
CREATE TABLE workflow_runs (
    id          INTEGER PRIMARY KEY,
    workflow_id TEXT    NOT NULL REFERENCES workflows (id) ON DELETE CASCADE,
    trigger     TEXT    NOT NULL,
    started_by  TEXT    NOT NULL,
    started_at  INTEGER NOT NULL,
    ended_at    INTEGER NOT NULL,
    outcome     TEXT    NOT NULL,
    error       TEXT    NOT NULL,
    steps       TEXT    NOT NULL DEFAULT '[]',
    data        TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX workflow_runs_workflow ON workflow_runs (workflow_id, id);

-- The log of the master and its agents, see the logs package. time is in milliseconds,
-- level that of Go's log/slog: -4 debug, 0 info, 4 warn, 8 error. node_id and server_id
-- tell what an entry is about, which decides who may see it. Entries outlive their nodes
-- and servers, so they keep the names these had. attrs is a JSON object of strings. size is
-- about the bytes an entry takes in the database, which the logs package keeps under the limit
-- of the settings: its texts, the IDs again in their index, and 64 bytes for the numbers, the
-- row and the index of the time.
CREATE TABLE log_entries (
    id          INTEGER PRIMARY KEY,
    time        INTEGER NOT NULL,
    level       INTEGER NOT NULL,
    source      TEXT    NOT NULL,
    category    TEXT    NOT NULL,
    message     TEXT    NOT NULL,
    username    TEXT    NOT NULL,
    node_id     TEXT    NOT NULL,
    node_name   TEXT    NOT NULL,
    server_id   TEXT    NOT NULL,
    server_name TEXT    NOT NULL,
    attrs       TEXT    NOT NULL,
    size        INTEGER GENERATED ALWAYS AS (
        octet_length(source) + octet_length(category) + octet_length(message) + octet_length(username) +
        octet_length(node_name) + octet_length(server_name) + octet_length(attrs) +
        2 * (octet_length(node_id) + octet_length(server_id)) + 64
    ) VIRTUAL
);
CREATE INDEX log_entries_time ON log_entries (time);
CREATE INDEX log_entries_target ON log_entries (node_id, server_id);

-- The number of entries and their size in all, kept up to date by the triggers, so that pruning
-- needn't count them. Entries are only added and deleted, never changed.
CREATE TABLE log_size (
    entries INTEGER NOT NULL,
    bytes   INTEGER NOT NULL
);
INSERT INTO log_size (entries, bytes) VALUES (0, 0);

CREATE TRIGGER log_entries_added AFTER INSERT ON log_entries BEGIN
    UPDATE log_size SET entries = entries + 1, bytes = bytes + NEW.size;
END;

CREATE TRIGGER log_entries_deleted AFTER DELETE ON log_entries BEGIN
    UPDATE log_size SET entries = entries - 1, bytes = bytes - OLD.size;
END;

-- Where the master continues reading the log of a node's agent.
CREATE TABLE log_cursors (
    node_id TEXT    PRIMARY KEY REFERENCES nodes (id) ON DELETE CASCADE,
    boot    TEXT    NOT NULL,
    seq     INTEGER NOT NULL
);

-- Channels send notifications to Discord, Slack, a webhook or by mail. secret is the URL of a
-- webhook or the password of the mail server, which the API never returns; email holds the
-- other settings of a mail channel as JSON. Channels send up to messages_per_minute messages a
-- minute after the first few, with up to entries_per_message entries each, and give times in
-- time_zone, an IANA time zone or empty for UTC.
CREATE TABLE notification_channels (
    id                  TEXT    PRIMARY KEY,
    name                TEXT    NOT NULL UNIQUE,
    kind                TEXT    NOT NULL,
    email               TEXT    NOT NULL DEFAULT '',
    secret              TEXT    NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    messages_per_minute INTEGER NOT NULL DEFAULT 1,
    entries_per_message INTEGER NOT NULL DEFAULT 10,
    time_zone           TEXT    NOT NULL DEFAULT ''
);

-- Rules send the new log entries of at least a level to a channel: of the categories in the JSON
-- array categories (all if it is empty), and of a node or server, of the servers with a tag or of
-- a network if they are set.
CREATE TABLE notification_rules (
    id         TEXT    PRIMARY KEY,
    channel_id TEXT    NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    enabled    INTEGER NOT NULL,
    level      TEXT    NOT NULL,
    categories TEXT    NOT NULL,
    node_id    TEXT    NOT NULL DEFAULT '',
    server_id  TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    tag        TEXT    NOT NULL DEFAULT '',
    network_id TEXT    NOT NULL DEFAULT ''
);

-- What nodes and their running servers used, measured every minute and kept for a week.
-- An empty server_id stands for the node itself. cpu_millis is in thousandths of a core,
-- the network columns in bytes per second; players and tps are NULL if unknown.
CREATE TABLE usage_samples (
    time         INTEGER NOT NULL,
    node_id      TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id    TEXT    NOT NULL,
    cpu_millis   INTEGER NOT NULL,
    memory_bytes INTEGER NOT NULL,
    net_received INTEGER NOT NULL,
    net_sent     INTEGER NOT NULL,
    disk_bytes   INTEGER NOT NULL,
    players      INTEGER,
    tps          REAL
);
CREATE INDEX usage_samples_target ON usage_samples (node_id, server_id, time);
CREATE INDEX usage_samples_time ON usage_samples (time);

-- What the running datastores used, measured every minute and kept for a week like the usage of
-- servers. cpu_millis is in thousandths of a core; connections is NULL while a datastore
-- couldn't tell, e.g. as it started.
CREATE TABLE datastore_usage (
    time         INTEGER NOT NULL,
    datastore_id TEXT    NOT NULL REFERENCES datastores (id) ON DELETE CASCADE,
    cpu_millis   INTEGER NOT NULL,
    memory_bytes INTEGER NOT NULL,
    connections  INTEGER,
    disk_bytes   INTEGER NOT NULL
);
CREATE INDEX datastore_usage_target ON datastore_usage (datastore_id, time);
CREATE INDEX datastore_usage_time ON datastore_usage (time);

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
CREATE INDEX datastore_usage_hours_time ON datastore_usage_hours (time);

-- Thresholds of usage that nodes and servers have instead of the defaults of the master's settings,
-- as a JSON object by measure, e.g. {"cpu": {"value": 95, "minutes": 15}}; measures it leaves out
-- use the defaults. An empty server_id stands for the node itself. Like their usage, the master
-- keeps them by node and server.
CREATE TABLE usage_thresholds (
    node_id    TEXT NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id  TEXT NOT NULL,
    thresholds TEXT NOT NULL,
    PRIMARY KEY (node_id, server_id)
);

-- Where players were online, from the names of the players of game servers that the agents measure every minute: a
-- row per player, server and day (days since 1970 in UTC), with when the player was first and last seen that day and
-- for how many minutes. Names match regardless of case, as in Minecraft. Like the log, rows are kept for its retention.
CREATE TABLE player_sightings (
    name       TEXT    NOT NULL COLLATE NOCASE,
    node_id    TEXT    NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    server_id  TEXT    NOT NULL,
    day        INTEGER NOT NULL,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    minutes    INTEGER NOT NULL,
    PRIMARY KEY (name, node_id, server_id, day)
) WITHOUT ROWID;
CREATE INDEX player_sightings_node ON player_sightings (node_id, day);
CREATE INDEX player_sightings_day ON player_sightings (day)
