package httpapi

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// officeRetentionPruneBytes returns the bytes that will be released by the
// version-retention rule when one new version is added. It is used to keep the
// quota check accurate: pruning old versions frees space in the same save.
func (s *Server) officeRetentionPruneBytes(ctx context.Context, documentID string, retention int) int64 {
	if retention <= 0 {
		return 0
	}
	rows, err := s.store.DB.Query(ctx, `
		SELECT size_bytes FROM office_document_versions
		WHERE document_id=$1 ORDER BY version DESC
	`, documentID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	sizes := []int64{}
	for rows.Next() {
		var size int64
		if rows.Scan(&size) == nil {
			sizes = append(sizes, size)
		}
	}
	return retentionPrunedBytes(sizes, retention)
}

func retentionPrunedBytes(sizes []int64, retention int) int64 {
	if retention <= 0 {
		return 0
	}
	var pruned int64
	for index, size := range sizes {
		if index+1 >= retention {
			pruned += size
		}
	}
	return pruned
}

// officePruneVersions removes versions beyond the retention limit and returns
// their storage keys so the caller can delete the blobs after the transaction
// commits. Content for the newest `retention` versions is never removed.
func officePruneVersions(ctx context.Context, tx pgx.Tx, documentID string, retention int) ([]string, error) {
	if retention <= 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, storage_key FROM office_document_versions
		WHERE document_id=$1 ORDER BY version DESC OFFSET $2
	`, documentID, retention)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	keys := []string{}
	for rows.Next() {
		var id, key string
		if rows.Scan(&id, &key) == nil {
			ids = append(ids, id)
			keys = append(keys, key)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM office_document_versions WHERE id = ANY($1::text[])`, ids); err != nil {
		return nil, err
	}
	return keys, nil
}
