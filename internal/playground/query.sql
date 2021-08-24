-- name: SelectOneByID :one
SELECT * FROM files WHERE `id` = ? LIMIT 1;