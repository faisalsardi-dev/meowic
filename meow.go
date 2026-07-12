// meow.go is the whatsmeow ambassador: the ONLY file in this codebase
// that imports go.mau.fi/whatsmeow. Everything the rest of the program
// can do to WhatsApp is one of the narrow functions below — the raw
// *whatsmeow.Client is never handed out. It also owns Structure(), the
// single output-formatting function every command ends with.
package main

import (
	"bufio"
	"bytes"
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

	"meowic/logic"
	"meowic/store"
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

func (m *Meow) HasSession() bool  { return m.cli.Store.ID != nil }
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
// The send restrictions live in logic/ and are enforced here — this is the
// single choke point every send passes through, before any network I/O.
// The actions/ layer stays generic (input-shape validation only) and does
// not apply any rule, so it can be reused by toolsets with other policies.
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
	resp, err := m.cli.SendMessage(m.ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return err
	}
	// WhatsApp never echoes a device's own sends back to it, so without this
	// the mirror would permanently miss messages sent through meowic — and an
	// LLM that can't see its own sent messages may conclude they failed and
	// resend. Record the confirmed send locally.
	sender := ""
	if m.cli.Store.ID != nil {
		sender = m.cli.Store.ID.ToNonAD().String()
	}
	_ = m.stores.Messages.UpsertChat(jid.String(), "", resp.Timestamp)
	_ = m.stores.Messages.InsertMessage(store.Message{
		ID:        string(resp.ID),
		ChatJID:   jid.String(),
		SenderJID: sender,
		FromMe:    true,
		Timestamp: resp.Timestamp,
		Text:      text,
	})
	return m.stores.Messages.SetMeta("last_send", resp.Timestamp.UTC().Format(time.RFC3339))
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

// ListNewsletterMessages fetches a channel's recent posts live and reduces
// each one through renderText — the same text-only rendering the mirror uses —
// instead of dumping raw whatsmeow structs (base64 thumbnails, hashes, CDN
// paths, message secrets) into the output.
func (m *Meow) ListNewsletterMessages(jidStr string, count int) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	msgs, err := m.cli.GetNewsletterMessages(m.ctx, jid, &whatsmeow.GetNewsletterMessagesParams{Count: count})
	if err != nil {
		return nil, err
	}
	type post struct {
		ID        string         `json:"id"`
		ServerID  int            `json:"server_id"`
		Type      string         `json:"type"`
		Timestamp time.Time      `json:"timestamp"`
		Views     int            `json:"views"`
		Reactions map[string]int `json:"reactions,omitempty"`
		Text      string         `json:"text"`
	}
	posts := make([]post, 0, len(msgs))
	for _, msg := range msgs {
		posts = append(posts, post{
			ID:        string(msg.MessageID),
			ServerID:  int(msg.MessageServerID),
			Type:      msg.Type,
			Timestamp: msg.Timestamp,
			Views:     msg.ViewsCount,
			Reactions: msg.ReactionCounts,
			Text:      renderText(msg.Message),
		})
	}
	return posts, nil
}

// GetPersonInfo merges a live GetUserInfo lookup (status text, profile
// picture presence, verified business name, LID linkage) with the local
// contact store (is_contact only — no names or phone fields by design)
// into one flat identity object for a person JID.
func (m *Meow) GetPersonInfo(jidStr string) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	infos, err := m.cli.GetUserInfo(m.ctx, []types.JID{jid})
	if err != nil {
		return nil, err
	}
	info := infos[jid]
	// The contact store is keyed by phone JID, so a @lid lookup would always
	// come back not-found even for a saved contact. Resolve the LID to its
	// phone JID through the local LID map first (local store, no network).
	contactJID := jid
	if jid.Server == types.HiddenUserServer {
		if pn, err := m.cli.Store.LIDs.GetPNForLID(m.ctx, jid); err == nil && pn.User != "" {
			contactJID = pn
		}
	}
	contact, err := m.cli.Store.Contacts.GetContact(m.ctx, contactJID)
	if err != nil {
		return nil, err
	}
	verified := ""
	if info.VerifiedName != nil {
		verified = info.VerifiedName.Details.GetVerifiedName()
	}
	lid := ""
	if info.LID.User != "" {
		lid = info.LID.String()
	}
	return struct {
		JID          string `json:"jid"`
		LID          string `json:"lid,omitempty"`
		IsContact    bool   `json:"is_contact"`
		Status       string `json:"status,omitempty"`
		VerifiedName string `json:"verified_name,omitempty"`
		HasImage     bool   `json:"has_image"`
	}{
		JID:          jid.String(),
		LID:          lid,
		IsContact:    contact.Found,
		Status:       info.Status,
		VerifiedName: verified,
		HasImage:     info.PictureID != "",
	}, nil
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
	// Chats table first, unconditionally: discovery (appearing in list-chats)
	// is decoupled from message storage, so channels and media-only chats
	// are listed even when nothing lands in the messages table.
	_ = m.stores.Messages.UpsertChat(e.Info.Chat.String(), chatName, e.Info.Timestamp)

	// Channels/newsletters are public server-side objects with their own live
	// read path (list-newsletter-messages); mirroring them would create a
	// second, divergent copy. Listed above, never stored below.
	if e.Info.Chat.Server == types.NewsletterServer {
		return
	}

	text := renderText(e.Message)
	if text == "" {
		return // unrecognized payloads (polls, locations, ...) are not mirrored
	}
	_ = m.stores.Messages.InsertMessage(store.Message{
		ID:        e.Info.ID,
		ChatJID:   e.Info.Chat.String(),
		SenderJID: e.Info.Sender.ToNonAD().String(),
		FromMe:    e.Info.IsFromMe,
		Timestamp: e.Info.Timestamp,
		Text:      text,
	})
}

// renderText reduces a message to the single text column of the mirror.
// Plain text passes through; media becomes a text-only reference row —
// a "[kind: metadata]" tag plus the caption, never any payload or path,
// so the tool's text-only posture is unchanged.
func renderText(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if t := msg.GetConversation(); t != "" {
		return t
	}
	if t := msg.GetExtendedTextMessage().GetText(); t != "" {
		return t
	}
	switch {
	case msg.GetImageMessage() != nil:
		img := msg.GetImageMessage()
		return withCaption(fmt.Sprintf("[image: %s]", fmtSize(img.GetFileLength())), img.GetCaption())
	case msg.GetVideoMessage() != nil:
		vid := msg.GetVideoMessage()
		return withCaption(fmt.Sprintf("[video: %s, %s]", fmtDur(vid.GetSeconds()), fmtSize(vid.GetFileLength())), vid.GetCaption())
	case msg.GetAudioMessage() != nil:
		aud := msg.GetAudioMessage()
		kind := "audio"
		if aud.GetPTT() {
			kind = "voice note"
		}
		return fmt.Sprintf("[%s: %s, %s]", kind, fmtDur(aud.GetSeconds()), fmtSize(aud.GetFileLength()))
	case msg.GetDocumentMessage() != nil:
		doc := msg.GetDocumentMessage()
		return withCaption(fmt.Sprintf("[document: %s, %s]", doc.GetFileName(), fmtSize(doc.GetFileLength())), doc.GetCaption())
	case msg.GetStickerMessage() != nil:
		return "[sticker]"
	}
	return ""
}

func withCaption(tag, caption string) string {
	if caption == "" {
		return tag
	}
	return tag + " " + caption
}

func fmtSize(bytes uint64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%dKB", bytes/(1<<10))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func fmtDur(seconds uint32) string {
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}

// parseUserJID parses a full JID. Sends are JID-only and logic.CheckSend has
// already required an "@s.whatsapp.net" suffix by the time we get here, so
// there is no bare-number normalization to do — the string always has a server.
func parseUserJID(s string) (types.JID, error) {
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
	// A plain encoder (not json.MarshalIndent) so SetEscapeHTML(false) keeps
	// <, > and & literal in error strings instead of <-style escapes.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if encErr := enc.Encode(env); encErr != nil {
		fmt.Printf("{\"ok\":false,\"error\":%q}\n", encErr.Error())
		return 1
	}
	fmt.Print(buf.String()) // Encode already appends a trailing newline
	if err != nil {
		return 1
	}
	return 0
}
