-- Items of Needs attention on the overview that each user hid, as a JSON list: what each is
-- about, until when it is hidden, and how it was if it is hidden until that changes.
CREATE TABLE hidden_items (
    user_id INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    items   TEXT    NOT NULL
)
