-- name: InsertHistory :exec
INSERT INTO playback_history(occurrence_id, guild_id, voice_id, session_id, bot_id, started_at, entry_json, automatic)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(occurrence_id) DO NOTHING;

-- name: HistoryAnchor :one
SELECT CAST(COALESCE(MAX(id), 0) AS INTEGER) AS anchor FROM playback_history WHERE guild_id = ? AND voice_id = ?;

-- name: CountHistory :one
SELECT COUNT(*) FROM playback_history WHERE guild_id = ? AND voice_id = ? AND id <= ?;

-- name: ListHistory :many
SELECT * FROM playback_history WHERE guild_id = ? AND voice_id = ? AND id <= ?
ORDER BY started_at DESC, id DESC LIMIT ? OFFSET ?;

-- name: GetHistory :one
SELECT * FROM playback_history WHERE guild_id = ? AND voice_id = ? AND id = ?;

-- name: PruneHistory :exec
DELETE FROM playback_history WHERE id IN (
    SELECT old.id FROM playback_history AS old WHERE old.guild_id = ? AND old.voice_id = ?
    ORDER BY old.started_at DESC, old.id DESC LIMIT -1 OFFSET ?
);
