package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// readCourseDetails returns only courses readable by actorID and attaches the
// course creator projection through the same queryer snapshot.
func readCourseDetails(ctx context.Context, queryer contentQueryer, actorID string, courseIDs []string) (map[string]course, error) {
	items := make(map[string]course, len(courseIDs))
	if len(courseIDs) == 0 {
		return items, nil
	}
	ids := make([]string, 0, len(courseIDs))
	seen := make(map[string]struct{}, len(courseIDs))
	for _, id := range courseIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	var placeholders strings.Builder
	args := make([]any, 0, len(ids)+2)
	for i, id := range ids {
		if i > 0 {
			placeholders.WriteByte(',')
		}
		placeholders.WriteByte('?')
		args = append(args, id)
	}
	args = append(args, actorID, actorID)
	rows, err := queryer.QueryContext(ctx, `SELECT c.id,c.owner_id,u.display_name,c.title,c.description,c.creator_name,c.visibility,
		COUNT(l.id),c.created_at,c.updated_at
		FROM courses c JOIN users u ON u.id=c.owner_id JOIN instance_settings s ON s.id=1
		LEFT JOIN course_lessons l ON l.course_id=c.id
		WHERE c.id IN (`+placeholders.String()+`) AND (c.owner_id=? OR (s.public_instance=1 AND c.visibility='public'))
		GROUP BY c.id ORDER BY c.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read course details: %w", err)
	}
	visibleIDs := make([]string, 0, len(ids))
	for rows.Next() {
		var item course
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.OwnerName, &item.Title, &item.Description, &item.CreatorName, &item.Visibility, &item.LessonCount, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan course details: %w", err)
		}
		items[item.ID] = item
		visibleIDs = append(visibleIDs, item.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate course details: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close course details: %w", err)
	}
	attributions, err := readCreatorAttributions(ctx, queryer, actorID, "course", visibleIDs)
	if err != nil {
		return nil, err
	}
	for id, item := range items {
		item.CreatorAttributions = attributions[id]
		items[id] = item
	}
	return items, nil
}

func readCourseDetail(ctx context.Context, queryer contentQueryer, actorID, courseID string) (course, error) {
	items, err := readCourseDetails(ctx, queryer, actorID, []string{courseID})
	if err != nil {
		return course{}, err
	}
	item, ok := items[courseID]
	if !ok {
		return course{}, sql.ErrNoRows
	}
	return item, nil
}
