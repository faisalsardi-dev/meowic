// meow.go is the whatsmeow ambassador: the ONLY file in this codebase
// that imports go.mau.fi/whatsmeow. Everything the rest of the program
// can do to WhatsApp is one of the narrow functions below — the raw
// *whatsmeow.Client is never handed out. It also owns Structure(), the
// single output-formatting function every command ends with.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/faisalsardi-dev/meowic/logic"
	"github.com/faisalsardi-dev/meowic/store"
)

// Meow owns the whatsmeow client and the local stores.
type Meow struct {
	ctx    context.Context
	cli    *whatsmeow.Client
	stores *store.Manager
}

func OpenMeow(ctx context.Context) (*Meow, error) {
	stores, err := store.Open(ctx)
	if err != nil {
		return nil, err
	}
	device, err := stores.Session.FirstDevice(ctx)
	if err != nil {
		stores.Close()
		return nil, err
	}
	m := &Meow{
		ctx:    ctx,
		cli:    whatsmeow.NewClient(device, waLog.Noop),
		stores: stores,
	}
	m.cli.AddEventHandler(m.handleEvent)
	return m, nil
}

func (m *Meow) Close() {
	if m.cli.IsConnected() {
		m.cli.Disconnect()
	}
	m.stores.Close()
}

func (m *Meow) HasSession() bool { return m.cli.Store.ID != nil }
func (m *Meow) IsConnected() bool { return m.cli.IsConnected() }
func (m *Meow) IsLoggedIn() bool  { return m.cli.IsLoggedIn() }

// Connect establishes the connection, running first-time linking-code
// pairing automatically when no session exists yet.
func (m *Meow) Connect() error {
	if m.cli.IsConnected() && m.cli.IsLoggedIn() {
		return nil
	}
	if !m.HasSession() {
		return m.pairAndConnect()
	}
	if err := m.cli.Connect(); err != nil {
		return err
	}
	if !m.cli.WaitForConnection(15 * time.Second) {
		return errors.New("timed out waiting for connection")
	}
	return nil
}

// pairAndConnect runs the first-run pairing flow: it asks for the
// account's phone number on stdin (prompt on stderr — stdout stays
// JSON-only), requests an 8-character linking code from WhatsApp, and
// blocks until the code is entered on the phone or the window times out.
func (m *Meow) pairAndConnect() error {
	if err := m.cli.Connect(); err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "no session found — enter this account's phone number in international format (e.g. 15551234567): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading phone number: %w", err)
	}
	phone := digitsOf(line)
	if phone == "" {
		return errors.New("no phone number entered")
	}
	code, err := m.cli.PairPhone(m.ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return fmt.Errorf("requesting linking code: %w", err)
	}
	fmt.Fprintf(os.Stderr, "on your phone: WhatsApp > Settings > Linked devices > Link a device > Link with phone number instead, then enter: %s\n", code)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if m.cli.IsLoggedIn() {
			m.afterFirstPair()
			return nil
		}
		select {
		case <-time.After(time.Second):
		case <-m.ctx.Done():
			return m.ctx.Err()
		}
	}
	return errors.New("pairing timed out — run the command again to retry")
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}


func (m *Meow) afterFirstPair() {
	// session.db holds plaintext auth material — lock it down.
	_ = os.Chmod(m.stores.SessionDBPath(), 0o700)
	fmt.Fprintln(os.Stderr, "paired successfully — waiting for the initial history sync...")
	// Initial history sync arrives as events shortly after pairing;
	// give it a window before the process exits and drops the connection.
	select {
	case <-time.After(15 * time.Second):
	case <-m.ctx.Done():
	}
}

func (m *Meow) ensureConnected() error { return m.Connect() }

// SendText sends a plain text message to an individual contact.
// The group-send rule lives in logic/ and is checked by send_people.go
// before this function is reached; it is re-checked here so no future
// caller inside this codebase can route around it.
func (m *Meow) SendText(to, text string) error {
	if err := logic.CheckSend(to); err != nil {
		return err
	}
	jid, err := parseUserJID(to)
	if err != nil {
		return err
	}
	if err := m.ensureConnected(); err != nil {
		return err
	}
	_, err = m.cli.SendMessage(m.ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return err
	}
	return m.stores.Messages.SetMeta("last_send", time.Now().UTC().Format(time.RFC3339))
}

func (m *Meow) GetGroupInfo(jidStr string) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	return m.cli.GetGroupInfo(m.ctx, jid)
}

func (m *Meow) GetNewsletterInfo(jidStr string) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	return m.cli.GetNewsletterInfo(m.ctx, jid)
}

func (m *Meow) ListNewsletterMessages(jidStr string, count int) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	return m.cli.GetNewsletterMessages(m.ctx, jid, &whatsmeow.GetNewsletterMessagesParams{Count: count})
}

// ListChats and ListMessages read the local mirror only — no network.
func (m *Meow) ListChats(limit int) ([]store.Chat, error) {
	return m.stores.Messages.ListChats(limit)
}

func (m *Meow) ListMessages(chatJID string, limit int) ([]store.Message, error) {
	return m.stores.Messages.ListMessages(chatJID, limit)
}

// handleEvent mirrors incoming traffic into messages.db while connected.
func (m *Meow) handleEvent(evt any) {
	switch e := evt.(type) {
	case *events.Message:
		m.storeMessage(e, "")
	case *events.HistorySync:
		for _, conv := range e.Data.GetConversations() {
			chatJID, err := types.ParseJID(conv.GetID())
			if err != nil {
				continue
			}
			for _, histMsg := range conv.GetMessages() {
				parsed, err := m.cli.ParseWebMessage(chatJID, histMsg.GetMessage())
				if err != nil {
					continue
				}
				m.storeMessage(parsed, conv.GetName())
			}
		}
		_ = m.stores.Messages.SetMeta("last_sync", time.Now().UTC().Format(time.RFC3339))
	}
}

func (m *Meow) storeMessage(e *events.Message, chatName string) {
	text := textOf(e.Message)
	if text == "" {
		return // text-only tool: media and other payloads are not mirrored
	}
	_ = m.stores.Messages.InsertMessage(store.Message{
		ID:        e.Info.ID,
		ChatJID:   e.Info.Chat.String(),
		SenderJID: e.Info.Sender.ToNonAD().String(),
		FromMe:    e.Info.IsFromMe,
		Timestamp: e.Info.Timestamp,
		Text:      text,
	})
	_ = m.stores.Messages.UpsertChat(e.Info.Chat.String(), chatName, e.Info.Timestamp)
}

func textOf(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if t := msg.GetConversation(); t != "" {
		return t
	}
	return msg.GetExtendedTextMessage().GetText()
}

func parseUserJID(s string) (types.JID, error) {
	if !strings.ContainsRune(s, '@') {
		s += "@" + types.DefaultUserServer
	}
	return types.ParseJID(s)
}

// Structure is the single output choke point: every command's result —
// success or failure — is printed by this function as JSON on stdout.
// It returns the process exit code.
func Structure(data any, err error) int {
	type envelope struct {
		OK    bool   `json:"ok"`
		Data  any    `json:"data,omitempty"`
		Error string `json:"error,omitempty"`
	}
	env := envelope{OK: err == nil, Data: data}
	if err != nil {
		env.Error = err.Error()
	}
	out, marshalErr := json.MarshalIndent(env, "", "  ")
	if marshalErr != nil {
		fmt.Printf("{\"ok\":false,\"error\":%q}\n", marshalErr.Error())
		return 1
	}
	fmt.Println(string(out))
	if err != nil {
		return 1
	}
	return 0
}
