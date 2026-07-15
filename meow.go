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
	// Pairing is interactive: it prompts for a phone number on stdin and needs
	// a human to type a linking code on the phone. Refuse to start it when
	// stdin is not a terminal — under the MCP wrapper (or any pipe) stdin is
	// never fed, so prompting would just block until the caller's timeout, and
	// piped bytes could be misread as a phone number and request a real code.
	// Fail closed with a clear message instead; pairing is a one-time terminal step.
	if stat, err := os.Stdin.Stat(); err != nil || stat.Mode()&os.ModeCharDevice == 0 {
		return errors.New("no session found and stdin is not a terminal — pairing is a one-time interactive step: run ./meowic doctor in a terminal to pair")
	}
	if err := m.cli.Connect(); err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "no session found — enter this account's phone number in international format (e.g. 15551234567 or 966512345678): ")
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
	// session.db holds plaintext auth material — lock it down (store.Open also
	// does this on every run; keep it here so it applies the moment we pair).
	_ = os.Chmod(m.stores.SessionDBPath(), 0o600)
	fmt.Fprintln(os.Stderr, "paired successfully — waiting for the initial history sync...")
	// Initial history sync arrives as events shortly after pairing;
	// give it a window before the process exits and drops the connection.
	select {
	case <-time.After(15 * time.Second):
	case <-m.ctx.Done():
	}
}

func (m *Meow) ensureConnected() error { return m.Connect() }

// opCtx bounds a single network operation so one hung request can't consume the
// whole caller budget (the MCP wrapper's exec timeout). Pairing is exempt — it
// uses m.ctx directly for its own multi-minute linking-code window.
func (m *Meow) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(m.ctx, 30*time.Second)
}

// readOnly reports whether the runtime read-only switch is engaged
// (env MEOWIC_READONLY set to anything other than "" / "0" / "false").
// Unlike the logic/ send policy — which is compile-time and immovable — this
// is a runtime flag by design, but it can only ever TIGHTEN: it disables the
// single write entirely and can never loosen who may be messaged. Reads are
// unaffected (they never reach SendText).
func readOnly() bool {
	v := strings.ToLower(os.Getenv("MEOWIC_READONLY"))
	return v != "" && v != "0" && v != "false"
}

// SendText sends a plain text message to an individual contact.
// The send restrictions live in logic/ and are enforced here — this is the
// single choke point every send passes through, before any network I/O.
// A runtime read-only switch (MEOWIC_READONLY) is checked first: when set it
// blocks every send, a level that can only clamp further, never loosen logic/.
// The actions/ layer stays generic (input-shape validation only) and does
// not apply any rule, so it can be reused by toolsets with other policies.
// SendText returns a non-fatal warning string alongside the error: the send
// itself is irreversible, so a failure to record the confirmed send in the
// local mirror must NOT be reported as a send error (that would invite a
// resend). Instead it comes back as a warning the caller surfaces to the LLM.
func (m *Meow) SendText(to, text string) (warning string, err error) {
	if readOnly() {
		return "", errors.New("read-only mode: sending is disabled (MEOWIC_READONLY is set)")
	}
	if err := logic.CheckSend(to); err != nil {
		return "", err
	}
	jid, err := parseUserJID(to)
	if err != nil {
		return "", err
	}
	if err := m.ensureConnected(); err != nil {
		return "", err
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	resp, err := m.cli.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return "", err
	}
	// WhatsApp never echoes a device's own sends back to it, so without this
	// the mirror would permanently miss messages sent through meowic — and an
	// LLM that can't see its own sent messages may conclude they failed and
	// resend. Record the confirmed send locally. If that write fails the send
	// still happened: warn (so the LLM won't resend on a missing echo) rather
	// than error.
	sender := ""
	if m.cli.Store.ID != nil {
		sender = m.cli.Store.ID.ToNonAD().String()
	}
	_ = m.stores.Messages.UpsertChat(jid.String(), "", resp.Timestamp)
	if werr := m.stores.Messages.InsertMessage(store.Message{
		ID:        string(resp.ID),
		ChatJID:   jid.String(),
		SenderJID: sender,
		FromMe:    true,
		Timestamp: resp.Timestamp,
		Text:      text,
	}); werr != nil {
		warning = "message was sent, but recording it in the local mirror failed; it may not appear in list-messages — do NOT resend"
	}
	_ = m.stores.Messages.SetMeta("last_send", resp.Timestamp.UTC().Format(time.RFC3339))
	return warning, nil
}

// GetGroupInfo returns a reduced view of a group's metadata. The raw
// whatsmeow *types.GroupInfo is deliberately NOT returned: it carries the
// owner's phone number and, per participant, BOTH the phone number and the
// LID — a hidden-number cross-link the text-only posture must not leak into
// output. Only vetted, non-cross-linking fields are emitted; each participant
// is reduced to its primary addressing JID plus admin flags.
func (m *Meow) GetGroupInfo(jidStr string) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	gi, err := m.cli.GetGroupInfo(ctx, jid)
	if err != nil {
		return nil, err
	}
	type participant struct {
		JID          string `json:"jid"`
		IsAdmin      bool   `json:"is_admin"`
		IsSuperAdmin bool   `json:"is_super_admin"`
	}
	parts := make([]participant, 0, len(gi.Participants))
	for _, p := range gi.Participants {
		parts = append(parts, participant{
			JID:          p.JID.String(),
			IsAdmin:      p.IsAdmin,
			IsSuperAdmin: p.IsSuperAdmin,
		})
	}
	count := gi.ParticipantCount
	if count == 0 {
		count = len(gi.Participants)
	}
	return struct {
		JID              string        `json:"jid"`
		Name             string        `json:"name"`
		Topic            string        `json:"topic,omitempty"`
		Created          time.Time     `json:"created"`
		IsAnnounce       bool          `json:"is_announce"`  // only admins may post
		IsLocked         bool          `json:"is_locked"`    // only admins may edit settings
		IsEphemeral      bool          `json:"is_ephemeral"` // disappearing messages on
		IsCommunity      bool          `json:"is_community"` // a community parent group
		ParticipantCount int           `json:"participant_count"`
		Participants     []participant `json:"participants"`
	}{
		JID:              gi.JID.String(),
		Name:             gi.Name,
		Topic:            gi.Topic,
		Created:          gi.GroupCreated,
		IsAnnounce:       gi.IsAnnounce,
		IsLocked:         gi.IsLocked,
		IsEphemeral:      gi.IsEphemeral,
		IsCommunity:      gi.IsParent,
		ParticipantCount: count,
		Participants:     parts,
	}, nil
}

// GetNewsletterInfo returns a reduced view of a channel's metadata. As with
// GetGroupInfo, the raw *types.NewsletterMetadata is NOT returned: it carries
// the invite code (a shareable join link) and profile-picture CDN URLs, which
// the text-only posture withholds. Only vetted fields are emitted.
func (m *Meow) GetNewsletterInfo(jidStr string) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	nm, err := m.cli.GetNewsletterInfo(ctx, jid)
	if err != nil {
		return nil, err
	}
	role := ""
	muted := false
	if nm.ViewerMeta != nil {
		role = string(nm.ViewerMeta.Role)
		muted = nm.ViewerMeta.Mute == types.NewsletterMuteOn
	}
	return struct {
		JID             string    `json:"jid"`
		Name            string    `json:"name"`
		Description     string    `json:"description,omitempty"`
		SubscriberCount int       `json:"subscriber_count"`
		Verified        bool      `json:"verified"`
		Created         time.Time `json:"created"`
		Role            string    `json:"role,omitempty"`
		Muted           bool      `json:"muted"`
	}{
		JID:             nm.ID.String(),
		Name:            nm.ThreadMeta.Name.Text,
		Description:     nm.ThreadMeta.Description.Text,
		SubscriberCount: nm.ThreadMeta.SubscriberCount,
		Verified:        nm.ThreadMeta.VerificationState == types.NewsletterVerificationStateVerified,
		Created:         nm.ThreadMeta.CreationTime.Time,
		Role:            role,
		Muted:           muted,
	}, nil
}

// ListNewsletterMessages fetches a channel's recent posts live — the backup
// read path for channels now that storeMessage also mirrors them into
// messages.db (list-messages). It reduces each post through renderText — the
// same text-only rendering the mirror uses — instead of dumping raw whatsmeow
// structs (base64 thumbnails, hashes, CDN paths, message secrets) into output.
func (m *Meow) ListNewsletterMessages(jidStr string, count int) (any, error) {
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return nil, err
	}
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	ctx, cancel := m.opCtx()
	defer cancel()
	msgs, err := m.cli.GetNewsletterMessages(ctx, jid, &whatsmeow.GetNewsletterMessagesParams{Count: count})
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
	ctx, cancel := m.opCtx()
	defer cancel()
	infos, err := m.cli.GetUserInfo(ctx, []types.JID{jid})
	if err != nil {
		return nil, err
	}
	// whatsmeow may key the response by a normalized JID rather than the exact
	// query JID; fall back to the sole returned entry so a real identity isn't
	// mistaken for not-found. found=false distinguishes "WhatsApp returned
	// nothing" from a genuine but blank profile.
	info, found := infos[jid]
	if !found && len(infos) == 1 {
		for _, v := range infos {
			info = v
			found = true
		}
	}
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
		Found        bool   `json:"found"`
		LID          string `json:"lid,omitempty"`
		IsContact    bool   `json:"is_contact"`
		Status       string `json:"status,omitempty"`
		VerifiedName string `json:"verified_name,omitempty"`
		HasImage     bool   `json:"has_image"`
	}{
		JID:          jid.String(),
		Found:        found,
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
	// Revokes (delete-for-everyone) and edits arrive as protocol messages that
	// target an EARLIER message by ID. Keep the mirror honest: drop the row on a
	// revoke, replace its text on an edit — otherwise the LLM keeps reading
	// content the other party has since deleted or changed. Handled before the
	// chat upsert so a revoke/edit never resurfaces or creates a chat row.
	// (Historical edits from a history sync arrive already resolved to their
	// final content via ParseWebMessage, so they take the normal insert path.)
	if pm := e.Message.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			if id := pm.GetKey().GetID(); id != "" {
				_ = m.stores.Messages.DeleteMessage(e.Info.Chat.String(), id)
			}
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			id := pm.GetKey().GetID()
			if text := renderText(pm.GetEditedMessage()); id != "" && text != "" {
				_ = m.stores.Messages.UpdateMessageText(e.Info.Chat.String(), id, text)
			}
		}
		return // no protocol message carries a new chat-content row of its own
	}

	// Chats table first, unconditionally: discovery (appearing in list-chats)
	// is decoupled from message storage, so channels and media-only chats
	// are listed even when nothing lands in the messages table.
	_ = m.stores.Messages.UpsertChat(e.Info.Chat.String(), chatName, e.Info.Timestamp)

	// Channels/newsletters are mirrored like any other chat so list-messages
	// surfaces them offline; list-newsletter-messages stays as the live backup
	// read path. (Earlier this early-returned to avoid a second copy; the user
	// reversed that — a local mirror of channel posts is now wanted.)
	text := renderText(e.Message)
	if text == "" {
		return // pure signalling (reactions, revokes/edits) leaves no row
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
	// Pure signalling payloads carry no standalone content and are meant to
	// leave no row: reactions, and protocol messages (revokes, edits, history
	// sync control). Everything else is a real message we simply don't model
	// yet (polls, locations, contact cards, ...) — mark it so the LLM sees a
	// message existed instead of an invisible gap, without storing any payload.
	if msg.GetReactionMessage() != nil || msg.GetProtocolMessage() != nil {
		return ""
	}
	return "[unsupported message]"
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
// already required an "@s.whatsapp.net" or "@lid" suffix by the time we get
// here, so there is no bare-number normalization to do — the string always has
// a server, and types.ParseJID handles both individual servers.
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
