-- name: CreateThread :one
INSERT INTO comment_threads (thread_id, artifact_id, anchor, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetThread :one
SELECT * FROM comment_threads WHERE thread_id = $1;

-- name: ListThreadsByArtifact :many
SELECT * FROM comment_threads
WHERE artifact_id = $1
ORDER BY created_at;

-- name: ListCommentsByArtifact :many
SELECT c.* FROM comments c
JOIN comment_threads t ON c.thread_id = t.thread_id
WHERE t.artifact_id = $1 AND c.deleted_at IS NULL
ORDER BY c.created_at;

-- name: ListCommentsByThread :many
SELECT * FROM comments
WHERE thread_id = $1 AND deleted_at IS NULL
ORDER BY created_at;

-- name: AddComment :one
INSERT INTO comments (comment_id, thread_id, author, body, author_name, author_picture)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: SetThreadStatus :execrows
UPDATE comment_threads
SET status = $2, resolved_by = $3, resolved_at = $4
WHERE thread_id = $1;

-- name: GetComment :one
SELECT * FROM comments WHERE comment_id = $1;

-- name: DeleteComment :execrows
UPDATE comments SET deleted_at = now()
WHERE comment_id = $1 AND deleted_at IS NULL;

-- name: CountLiveComments :one
SELECT COUNT(*) FROM comments WHERE thread_id = $1 AND deleted_at IS NULL;

-- name: UpdateComment :one
UPDATE comments SET body = $2, edited_at = now()
WHERE comment_id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: DeleteThread :execrows
DELETE FROM comment_threads WHERE thread_id = $1;
