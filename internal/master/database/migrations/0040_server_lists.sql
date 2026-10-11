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
)
