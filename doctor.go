package main

// HealthReport is the ONLY exported function in this file. It bundles
// every health/self-check into one JSON blob, connection status first:
// reading just the first two keys tells you if the system is basically OK.
func HealthReport(m *Meow) (any, error) {
	type storeHealth struct {
		SessionDB  string `json:"session_db"`
		MessagesDB string `json:"messages_db"`
		Chats      int    `json:"chats"`
		Messages   int    `json:"messages"`
		Error      string `json:"error,omitempty"`
	}
	type report struct {
		Connected     bool        `json:"connected"`
		LoggedIn      bool        `json:"logged_in"`
		SessionExists bool        `json:"session_exists"`
		ConnectError  string      `json:"connect_error,omitempty"`
		Store         storeHealth `json:"store"`
		LastSync      string      `json:"last_sync,omitempty"`
		LastSend      string      `json:"last_send,omitempty"`
	}

	r := &report{}
	// On a first run this triggers linking-code pairing (prompt on stderr).
	if err := m.Connect(); err != nil {
		r.ConnectError = err.Error()
	}
	r.Connected = m.IsConnected()
	r.LoggedIn = m.IsLoggedIn()
	r.SessionExists = m.HasSession()

	r.Store.SessionDB = m.stores.SessionDBPath()
	r.Store.MessagesDB = m.stores.Messages.Path()
	chats, messages, err := m.stores.Messages.Counts()
	if err != nil {
		r.Store.Error = err.Error()
	}
	r.Store.Chats = chats
	r.Store.Messages = messages

	r.LastSync, _ = m.stores.Messages.GetMeta("last_sync")
	r.LastSend, _ = m.stores.Messages.GetMeta("last_send")
	return r, nil
}
