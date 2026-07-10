// meow.go is the whatsmeow ambassador: the ONLY file in this codebase
// that imports go.mau.fi/whatsmeow. Everything the rest of the program
// can do to WhatsApp is one of the narrow functions below — the raw
// *whatsmeow.Client is never handed out. It also owns Structure(), the
// single output-formatting function every command ends with.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
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

// Connect establishes the connection, running first-time QR pairing
// automatically when no session exists yet.
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

// pairAndConnect runs the first-run QR flow: the QR code is served on a
// loopback-only browser page (stdout stays JSON-only) and we block until
// the phone scans it or the pairing window times out. If the local page
// can't be started, it falls back to rendering on stderr as before.
func (m *Meow) pairAndConnect() error {
	qrChan, err := m.cli.GetQRChannel(m.ctx)
	if err != nil {
		return err
	}
	if err := m.cli.Connect(); err != nil {
		return err
	}
	web, webErr := startPairQRWeb()
	if webErr != nil {
		fmt.Fprintln(os.Stderr, "no session found — scan the QR code with WhatsApp (Settings > Linked devices > Link a device)")
	} else {
		defer web.close()
		fmt.Fprintf(os.Stderr, "no session found — scan the QR code at %s with WhatsApp (Settings > Linked devices > Link a device)\n", web.url)
		web.openBrowser()
	}
	for item := range qrChan {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			if webErr != nil {
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stderr)
			} else {
				web.setCode(item.Code)
			}
		case whatsmeow.QRChannelSuccess.Event:
			if webErr == nil {
				web.setState("paired")
			}
			m.afterFirstPair()
			return nil
		case whatsmeow.QRChannelTimeout.Event:
			return errors.New("QR pairing timed out — run the command again to retry")
		default:
			if item.Error != nil {
				return fmt.Errorf("pairing failed: %w", item.Error)
			}
		}
	}
	return errors.New("QR channel closed before pairing completed")
}

// pairQRWeb shows the pairing QR in a local browser tab: the same
// half-block text qrterminal would print, in a <pre> so it stays
// copyable. Bound to 127.0.0.1 on an OS-chosen free port — never
// reachable from the network. WhatsApp rotates the code server-side,
// so the page polls and repaints only when the code actually changes.
type pairQRWeb struct {
	mu    sync.Mutex
	qr    string // half-block render of the current code
	state string // "waiting" | "paired"
	srv   *http.Server
	url   string
}

func startPairQRWeb() (*pairQRWeb, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	w := &pairQRWeb{state: "waiting", url: fmt.Sprintf("http://%s/", ln.Addr())}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write([]byte(pairQRPage))
	})
	mux.HandleFunc("/qr.json", func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		payload := map[string]string{"state": w.state, "qr": w.qr}
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		rw.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(rw).Encode(payload)
	})
	w.srv = &http.Server{Handler: mux}
	go func() { _ = w.srv.Serve(ln) }()
	return w, nil
}

func (w *pairQRWeb) close() { _ = w.srv.Close() }

func (w *pairQRWeb) setCode(code string) {
	var buf bytes.Buffer
	qrterminal.GenerateHalfBlock(code, qrterminal.L, &buf)
	w.mu.Lock()
	w.qr = buf.String()
	w.mu.Unlock()
}

func (w *pairQRWeb) setState(state string) {
	w.mu.Lock()
	w.state = state
	w.mu.Unlock()
}

// openBrowser is best-effort: pairing works fine with the printed URL.
func (w *pairQRWeb) openBrowser() {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", w.url).Start()
	case "linux":
		_ = exec.Command("xdg-open", w.url).Start()
	}
}

const pairQRPage = `<!doctype html>
<meta charset="utf-8">
<title>meowic — link WhatsApp</title>
<style>
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
         background:#111b21; color:#e9edef; font:16px/1.5 -apple-system, system-ui, sans-serif; }
  main { text-align:center; padding:2rem; }
  pre  { display:inline-block; font:14px/1 monospace; letter-spacing:0;
         color:#fff; background:#000; padding:1.5em; }
  #msg { margin-top:1rem; color:#8696a0; }
  .ok  { color:#00a884; font-weight:600; }
</style>
<main>
  <h1>Scan with WhatsApp</h1>
  <pre id="qr">waiting for QR code…</pre>
  <p id="msg">Settings &gt; Linked devices &gt; Link a device</p>
</main>
<script>
  const qr = document.getElementById('qr'), msg = document.getElementById('msg');
  const tick = async () => {
    try {
      const s = await (await fetch('/qr.json')).json();
      if (s.state === 'paired') {
        qr.remove();
        msg.textContent = 'Paired successfully — you can close this tab.';
        msg.className = 'ok';
        clearInterval(timer);
        return;
      }
      if (s.qr && s.qr !== qr.textContent) qr.textContent = s.qr;
    } catch {
      msg.textContent = 'meowic exited — run doctor again to pair.';
      clearInterval(timer);
    }
  };
  const timer = setInterval(tick, 2000);
  tick();
</script>
`

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
