CREATE TABLE IF NOT EXISTS playback_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    occurrence_id TEXT NOT NULL UNIQUE,
    guild_id TEXT NOT NULL,
    voice_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    bot_id TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    entry_json BLOB NOT NULL,
    automatic INTEGER NOT NULL CHECK (automatic IN (0, 1))
);
CREATE INDEX IF NOT EXISTS history_voice_started
    ON playback_history(guild_id, voice_id, started_at DESC, id DESC);
