package store

import (
	"context"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	wstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// SessionStore wraps whatsmeow's sqlstore container (session.db), which
// holds the device identity, keys, and auth state. It never leaves this
// package except as the *wstore.Device handed to meow.go.
type SessionStore struct {
	container *sqlstore.Container
	path      string
}

func OpenSession(ctx context.Context, path string) (*SessionStore, error) {
	// Name shown in WhatsApp's Linked Devices list (default "whatsmeow");
	// only takes effect at pairing time, so set it before any QR flow.
	wstore.DeviceProps.Os = proto.String("Desktop")
	wstore.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	container, err := sqlstore.New(ctx, "sqlite3", "file:"+path+"?_foreign_keys=on&_busy_timeout=5000", waLog.Noop)
	if err != nil {
		return nil, err
	}
	return &SessionStore{container: container, path: path}, nil
}

// FirstDevice returns the stored device, creating a fresh unpaired one
// on first run.
func (s *SessionStore) FirstDevice(ctx context.Context) (*wstore.Device, error) {
	return s.container.GetFirstDevice(ctx)
}

func (s *SessionStore) Path() string { return s.path }

func (s *SessionStore) Close() error { return s.container.Close() }
