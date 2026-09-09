package sessioncatalog

import (
	"context"
	"strings"
)

const knownTopicIDBatchSize = 100

// KnownTopicIDs reports which of the given topic ids still resolve to a live
// catalog topic in any scope/workspace. A topic counts as known when at least
// one session row references it — the same tombstone semantics GetTopic
// applies. Callers use it to drop stale member ids from persisted group
// rosters before handing them to the UI (#9518).
func (c *Catalog) KnownTopicIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	known := make(map[string]bool, len(ids))
	cleaned := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cleaned = append(cleaned, id)
	}
	for start := 0; start < len(cleaned); start += knownTopicIDBatchSize {
		end := min(start+knownTopicIDBatchSize, len(cleaned))
		batch := cleaned[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		rows, err := c.db.QueryContext(ctx, `SELECT DISTINCT t.topic_id
			FROM catalog_topics t
			WHERE t.topic_id IN (`+placeholders+`)
			  AND EXISTS (SELECT 1 FROM catalog_sessions s
			    WHERE s.scope=t.scope AND s.workspace_root=t.workspace_root AND s.topic_id=t.topic_id)`,
			args...)
		if err != nil {
			return known, err
		}
		scanErr := func() error {
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				known[id] = true
			}
			return rows.Err()
		}()
		if scanErr != nil {
			return known, scanErr
		}
	}
	return known, nil
}
