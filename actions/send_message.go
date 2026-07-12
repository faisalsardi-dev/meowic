package actions

import (
	"errors"
	"strings"
)

// SendMessage is the only write command: it sends a text message to a
// recipient (a full JID — no bare numbers). There is no media, file, or
// attachment parameter of any kind — the absence is deliberate and must
// stay that way.
//
// This function only validates input shape (recipient is a well-formed JID,
// text is non-empty). It applies no policy — restrictions such as which JIDs
// may be messaged live in logic/ and are enforced downstream, keeping actions/
// reusable across other Go builds without changing a line here.
func SendMessage(args []string, send func(to, text string) error) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("usage: send-people <jid> <message...>")
	}
	to := args[0]
	text := strings.Join(args[1:], " ")
	if err := validateJID(to); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("message text must not be empty")
	}
	if err := send(to, text); err != nil {
		return nil, err
	}
	return map[string]any{"status": "sent", "to": to}, nil
}
