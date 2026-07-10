package store

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Chat is one row of the synced chat list.
type Chat struct {
	JID             string    `json:"jid"`
	Name            string    `json:"name"`
	LastMessageTime time.Time `json:"last_message_time"`
}

// Message is one synced text message. Media is never stored: this tool
// is text-only by design.
type Message struct {
	ID        string    `json:"id"`
	ChatJID   string    `json:"chat_jid"`
	SenderJID string    `json:"sender_jid"`
	FromMe    bool      `json:"from_me"`
	Timestamp time.Time `json:"timestamp"`
	Text      string    `json:"text"`
}

// MessageStore is the local mirror of message history (messages.db),
// populated by the event handler in meow.go while connected.
type MessageStore struct {
	db   *sql.DB
	path string
}

func OpenMessages(path string) (*MessageStore, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chats (
			jid TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			last_message_time TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS messages (
			id TEXT NOT NULL,
			chat_jid TEXT NOT NULL,
			sender_jid TEXT NOT NULL,
			from_me INTEGER NOT NULL,
			timestamp TIMESTAMP NOT NULL,
			text TEXT NOT NULL,
			PRIMARY KEY (id, chat_jid)
		);
		CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
	`); err != nil {
		db.Close()
		return nil, err
	}
	return &MessageStore{db: db, path: path}, nil
}

func (s *MessageStore) Close() error { return s.db.Close() }

func (s *MessageStore) Path() string { return s.path }

// UpsertChat records a chat, keeping the newest name/timestamp seen.
func (s *MessageStore) UpsertChat(jid, name string, lastMessage time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE chats.name END,
			last_message_time = MAX(chats.last_message_time, excluded.last_message_time)
	`, jid, name, lastMessage.UTC())
	return err
}

func (s *MessageStore) InsertMessage(m Message) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO messages (id, chat_jid, sender_jid, from_me, timestamp, text)
		VALUES (?, ?, ?, ?, ?, ?)
	`, m.ID, m.ChatJID, m.SenderJID, m.FromMe, m.Timestamp.UTC(), m.Text)
	return err
}

func (s *MessageStore) ListChats(limit int) ([]Chat, error) {
	rows, err := s.db.Query(`
		SELECT jid, name, last_message_time FROM chats
		ORDER BY last_message_time DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := []Chat{}
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.JID, &c.Name, &c.LastMessageTime); err != nil {
			return nil, err
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

func (s *MessageStore) ListMessages(chatJID string, limit int) ([]Message, error) {
	rows, err := s.db.Query(`
		SELECT id, chat_jid, sender_jid, from_me, timestamp, text FROM messages
		WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT ?
	`, chatJID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ChatJID, &m.SenderJID, &m.FromMe, &m.Timestamp, &m.Text); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (s *MessageStore) SetMeta(key, value string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)`, key, value)
	return err
}

// GetMeta returns "" for a key that was never set.
func (s *MessageStore) GetMeta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *MessageStore) Counts() (chats, messages int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM chats`).Scan(&chats); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages)
	return
}
