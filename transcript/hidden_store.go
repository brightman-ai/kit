package transcript

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// SQLiteHiddenStore implements HiddenStore over the conversationDB. It is the
// ONLY mutable state in this package — and it only ever flips a soft-delete
// flag or stores a soft-rename. The runtime transcript (jsonl / deepwork
// session) is never touched (SSOT protection, SSOT-SESSION-LOADER.md §0).
//
// Table runtime_session_hidden(source, ssot_key UNIQUE, hidden, hidden_at,
// friendly_name) is created by storage.migrateConversationDB(); the
// (source, ssot_key) pair is the stable key (ssot_key = the SessionMeta.ID for
// that source). A row may exist with hidden=0 when it carries only a rename.
type SQLiteHiddenStore struct {
	db *sql.DB
}

// NewSQLiteHiddenStore binds a HiddenStore to the conversationDB handle.
func NewSQLiteHiddenStore(db *sql.DB) *SQLiteHiddenStore {
	return &SQLiteHiddenStore{db: db}
}

func (s *SQLiteHiddenStore) IsHidden(ctx context.Context, source, ssotKey string) (bool, error) {
	const q = `SELECT hidden FROM runtime_session_hidden WHERE source = ? AND ssot_key = ? LIMIT 1`
	var hidden int
	err := s.db.QueryRowContext(ctx, q, source, ssotKey).Scan(&hidden)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return hidden != 0, nil
}

func (s *SQLiteHiddenStore) HiddenSet(ctx context.Context, source string) (map[string]bool, error) {
	const q = `SELECT ssot_key FROM runtime_session_hidden WHERE source = ? AND hidden != 0`
	rows, err := s.db.QueryContext(ctx, q, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}

// Overlays returns hidden + friendly_name per ssot key for a source (one read).
func (s *SQLiteHiddenStore) Overlays(ctx context.Context, source string) (map[string]SessionOverlay, error) {
	const q = `SELECT ssot_key, hidden, friendly_name FROM runtime_session_hidden WHERE source = ?`
	rows, err := s.db.QueryContext(ctx, q, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SessionOverlay)
	for rows.Next() {
		var key, name string
		var hidden int
		if err := rows.Scan(&key, &hidden, &name); err != nil {
			return nil, err
		}
		out[key] = SessionOverlay{Hidden: hidden != 0, FriendlyName: name}
	}
	return out, rows.Err()
}

// SetHidden marks (hidden=true) or unmarks (hidden=false) a session. Idempotent.
// It only writes the deepwork-owned flag table; it does NOT delete any SSOT.
// A row carrying a friendly_name is preserved on un-hide (hidden set to 0); a
// row with neither hidden nor a rename is removed to keep the table lean.
func (s *SQLiteHiddenStore) SetHidden(ctx context.Context, source, ssotKey string, hidden bool) error {
	if hidden {
		const ins = `INSERT INTO runtime_session_hidden (source, ssot_key, hidden, hidden_at)
			VALUES (?, ?, 1, ?)
			ON CONFLICT(source, ssot_key) DO UPDATE SET hidden = 1, hidden_at = excluded.hidden_at`
		_, err := s.db.ExecContext(ctx, ins, source, ssotKey, time.Now().UTC())
		return err
	}
	// Un-hide: clear the flag but keep any rename; drop the row if now empty.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE runtime_session_hidden SET hidden = 0 WHERE source = ? AND ssot_key = ?`,
		source, ssotKey); err != nil {
		return err
	}
	return s.pruneIfEmpty(ctx, source, ssotKey)
}

// SetFriendlyName sets or clears the soft-rename. Idempotent. Preserves hidden.
// An empty name clears the rename; the row is removed if it then carries no
// state. The runtime transcript title is never modified (SSOT protection).
func (s *SQLiteHiddenStore) SetFriendlyName(ctx context.Context, source, ssotKey, name string) error {
	name = strings.TrimSpace(name)
	const ins = `INSERT INTO runtime_session_hidden (source, ssot_key, hidden, friendly_name)
		VALUES (?, ?, 0, ?)
		ON CONFLICT(source, ssot_key) DO UPDATE SET friendly_name = excluded.friendly_name`
	if _, err := s.db.ExecContext(ctx, ins, source, ssotKey, name); err != nil {
		return err
	}
	if name == "" {
		return s.pruneIfEmpty(ctx, source, ssotKey)
	}
	return nil
}

// pruneIfEmpty deletes the overlay row when it carries neither a hide nor a
// rename, keeping the table free of inert rows.
func (s *SQLiteHiddenStore) pruneIfEmpty(ctx context.Context, source, ssotKey string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM runtime_session_hidden WHERE source = ? AND ssot_key = ? AND hidden = 0 AND friendly_name = ''`,
		source, ssotKey)
	return err
}
