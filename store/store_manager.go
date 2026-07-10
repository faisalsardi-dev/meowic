package store

import (
	"context"
	"os"
	"path/filepath"
)

// DataDir is where both runtime databases live, relative to the working
// directory. It is gitignored.
const DataDir = "store/data"

// Manager opens and closes both databases and hands the refs to meow.go.
type Manager struct {
	Session  *SessionStore
	Messages *MessageStore
}

func Open(ctx context.Context) (*Manager, error) {
	if err := os.MkdirAll(DataDir, 0o700); err != nil {
		return nil, err
	}
	session, err := OpenSession(ctx, filepath.Join(DataDir, "session.db"))
	if err != nil {
		return nil, err
	}
	messages, err := OpenMessages(filepath.Join(DataDir, "messages.db"))
	if err != nil {
		session.Close()
		return nil, err
	}
	return &Manager{Session: session, Messages: messages}, nil
}

func (m *Manager) Close() {
	m.Messages.Close()
	m.Session.Close()
}

func (m *Manager) SessionDBPath() string { return m.Session.Path() }
