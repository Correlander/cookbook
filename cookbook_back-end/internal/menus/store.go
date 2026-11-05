package menus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Store never opens or commits a transaction. Callers can compose these
// operations with recipe authorization and creator attribution atomically.
type Store struct{}

func (Store) Create(ctx context.Context, tx *sql.Tx, in CreateInput) (Menu, error) {
	if err := ValidateCreate(in); err != nil {
		return Menu{}, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO meal_menus(id,owner_id,title,note,creator_name,version,created_at,updated_at) VALUES(?,?,?,?,?,1,?,?)`, in.ID, in.OwnerID, in.Title, in.Note, in.CreatorName, in.Now, in.Now)
	if err != nil {
		if isMenuLimitError(err) {
			return Menu{}, ErrLimit
		}
		return Menu{}, fmt.Errorf("insert menu reservation: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meal_menus WHERE owner_id=?`, in.OwnerID).Scan(&count); err != nil {
		return Menu{}, fmt.Errorf("count owner menus: %w", err)
	}
	if count > MaxMenusPerOwner {
		return Menu{}, ErrLimit
	}
	return readMenu(ctx, tx, in.ID, in.OwnerID)
}

func (Store) Reserve(ctx context.Context, tx *sql.Tx, id, owner string, version int, now string) (int, error) {
	if id == "" || owner == "" || version < 1 || now == "" {
		return 0, ErrInvalid
	}
	result, err := tx.ExecContext(ctx, `UPDATE meal_menus SET version=version+1,updated_at=? WHERE id=? AND owner_id=? AND version=?`, now, id, owner, version)
	if err != nil {
		return 0, fmt.Errorf("reserve menu version: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read menu reservation result: %w", err)
	}
	if changed == 1 {
		return version + 1, nil
	}
	var current int
	err = tx.QueryRowContext(ctx, `SELECT version FROM meal_menus WHERE id=? AND owner_id=?`, id, owner).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read menu version: %w", err)
	}
	return 0, ErrConflict
}

func (Store) Delete(ctx context.Context, tx *sql.Tx, id, owner string, version int) error {
	result, err := tx.ExecContext(ctx, `DELETE FROM meal_menus WHERE id=? AND owner_id=? AND version=?`, id, owner, version)
	if err != nil {
		return fmt.Errorf("delete menu: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read menu delete result: %w", err)
	}
	if changed == 1 {
		return nil
	}
	var found int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM meal_menus WHERE id=? AND owner_id=?`, id, owner).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check menu delete target: %w", err)
	}
	return ErrConflict
}

func (Store) Update(ctx context.Context, tx *sql.Tx, id, owner string, input UpdateInput) error {
	if err := ValidateUpdate(input); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE meal_menus SET title=?,note=?,updated_at=? WHERE id=? AND owner_id=?`, input.Title, input.Note, input.Now, id, owner)
	if err != nil {
		return fmt.Errorf("update menu fields: %w", err)
	}
	return nil
}

func (Store) AddEntry(ctx context.Context, tx *sql.Tx, in EntryInput) error {
	if err := ValidateEntry(in); err != nil {
		return err
	}
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position)+1,0) FROM meal_menu_entries WHERE menu_id=?`, in.MenuID).Scan(&position); err != nil {
		return fmt.Errorf("read menu entry position: %w", err)
	}
	if position >= MaxEntries {
		return ErrLimit
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id,label,note,serving_note) VALUES(?,?,?,?,?,?,?,?)`, in.ID, in.MenuID, position, nullable(in.LocalInstanceID), nullable(in.LocalRecipeID), in.Label, in.Note, in.ServingNote)
	if err != nil {
		if isMenuLimitError(err) {
			return ErrLimit
		}
		return fmt.Errorf("insert menu entry: %w", err)
	}
	return nil
}

func (Store) UpdateEntry(ctx context.Context, tx *sql.Tx, menuID, entryID string, in EntryInput) error {
	if err := ValidateEntry(in); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE meal_menu_entries SET local_instance_id=?,local_recipe_id=?,label=?,note=?,serving_note=? WHERE id=? AND menu_id=?`, nullable(in.LocalInstanceID), nullable(in.LocalRecipeID), in.Label, in.Note, in.ServingNote, entryID, menuID)
	if err != nil {
		return fmt.Errorf("update menu entry: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read menu entry update result: %w", err)
	}
	if changed == 0 {
		return ErrEntry
	}
	return nil
}

func (Store) RemoveEntry(ctx context.Context, tx *sql.Tx, menuID, entryID string) error {
	var items []Entry
	items, err := readEntries(ctx, tx, menuID)
	if err != nil {
		return err
	}
	found := false
	for i := range items {
		if items[i].ID == entryID {
			found = true
			break
		}
	}
	if !found {
		return ErrEntry
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meal_menu_entries WHERE id=? AND menu_id=?`, entryID, menuID); err != nil {
		return fmt.Errorf("remove menu entry: %w", err)
	}
	position := 0
	for _, item := range items {
		if item.ID == entryID {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meal_menu_entries SET position=? WHERE id=?`, position, item.ID); err != nil {
			return fmt.Errorf("compact menu entry order: %w", err)
		}
		position++
	}
	return nil
}

func (Store) Reorder(ctx context.Context, tx *sql.Tx, menuID string, ids []string) error {
	items, err := readEntries(ctx, tx, menuID)
	if err != nil {
		return err
	}
	if len(ids) != len(items) {
		return ErrInvalid
	}
	byID := make(map[string]Entry, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			return ErrInvalid
		}
		if _, ok := seen[id]; ok {
			return ErrInvalid
		}
		seen[id] = struct{}{}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM meal_menu_entries WHERE menu_id=?`, menuID); err != nil {
		return fmt.Errorf("clear menu order for reorder: %w", err)
	}
	for position, id := range ids {
		item := byID[id]
		if _, err := tx.ExecContext(ctx, `INSERT INTO meal_menu_entries(id,menu_id,position,local_instance_id,local_recipe_id,label,note,serving_note) VALUES(?,?,?,?,?,?,?,?)`, item.ID, menuID, position, nullable(item.LocalInstanceID), nullable(item.LocalRecipeID), item.Label, item.Note, item.ServingNote); err != nil {
			return fmt.Errorf("write menu order: %w", err)
		}
	}
	return nil
}

func (Store) Get(ctx context.Context, tx *sql.Tx, id, owner string) (Menu, error) {
	return readMenu(ctx, tx, id, owner)
}

func (Store) List(ctx context.Context, tx *sql.Tx, owner string, limit, offset int) ([]Menu, int, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
		return nil, 0, ErrInvalid
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meal_menus WHERE owner_id=?`, owner).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count menu page: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,owner_id,title,note,creator_name,version,created_at,updated_at FROM meal_menus WHERE owner_id=? ORDER BY updated_at DESC,id LIMIT ? OFFSET ?`, owner, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list menus: %w", err)
	}
	defer rows.Close()
	items := make([]Menu, 0, limit)
	ids := make([]string, 0, limit)
	for rows.Next() {
		var item Menu
		if err := rows.Scan(&item.ID, &item.OwnerID, &item.Title, &item.Note, &item.CreatorName, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, 0, err
		}
		ids = append(ids, item.ID)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, fmt.Errorf("close listed menus: %w", err)
	}
	entryMap, err := readEntriesForMenus(ctx, tx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range items {
		items[i].Entries = entryMap[items[i].ID]
	}
	return items, total, nil
}

func readMenu(ctx context.Context, tx *sql.Tx, id, owner string) (Menu, error) {
	var item Menu
	err := tx.QueryRowContext(ctx, `SELECT id,owner_id,title,note,creator_name,version,created_at,updated_at FROM meal_menus WHERE id=? AND owner_id=?`, id, owner).Scan(&item.ID, &item.OwnerID, &item.Title, &item.Note, &item.CreatorName, &item.Version, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Menu{}, ErrNotFound
	}
	if err != nil {
		return Menu{}, fmt.Errorf("read menu: %w", err)
	}
	item.Entries, err = readEntries(ctx, tx, id)
	return item, err
}

func readEntries(ctx context.Context, tx *sql.Tx, menuID string) ([]Entry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,menu_id,position,COALESCE(local_instance_id,''),COALESCE(local_recipe_id,''),label,note,serving_note FROM meal_menu_entries WHERE menu_id=? ORDER BY position,id`, menuID)
	if err != nil {
		return nil, fmt.Errorf("read menu entries: %w", err)
	}
	defer rows.Close()
	items := make([]Entry, 0)
	for rows.Next() {
		var item Entry
		if err := rows.Scan(&item.ID, &item.MenuID, &item.Position, &item.LocalInstanceID, &item.LocalRecipeID, &item.Label, &item.Note, &item.ServingNote); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate menu entries: %w", err)
	}
	return items, nil
}

func readEntriesForMenus(ctx context.Context, tx *sql.Tx, menuIDs []string) (map[string][]Entry, error) {
	result := make(map[string][]Entry, len(menuIDs))
	if len(menuIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, len(menuIDs))
	args := make([]any, len(menuIDs))
	for i, id := range menuIDs {
		placeholders[i] = "?"
		args[i] = id
		result[id] = make([]Entry, 0)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,menu_id,position,COALESCE(local_instance_id,''),COALESCE(local_recipe_id,''),label,note,serving_note FROM meal_menu_entries WHERE menu_id IN (`+strings.Join(placeholders, ",")+") ORDER BY menu_id,position,id", args...)
	if err != nil {
		return nil, fmt.Errorf("read listed menu entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item Entry
		if err := rows.Scan(&item.ID, &item.MenuID, &item.Position, &item.LocalInstanceID, &item.LocalRecipeID, &item.Label, &item.Note, &item.ServingNote); err != nil {
			return nil, err
		}
		result[item.MenuID] = append(result[item.MenuID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate listed menu entries: %w", err)
	}
	return result, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func isMenuLimitError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "meal menu account limit reached") || strings.Contains(err.Error(), "meal menu entry limit reached"))
}
