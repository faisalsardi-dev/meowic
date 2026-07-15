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
func SendMessage(args []string, send func(to, text string) (string, error)) (any, error) {
	if len(args) != 2 {
		return nil, errors.New(`usage: send-message <jid> <message> (quote the message: "hello there")`)
	}
	to := args[0]
	text := args[1]
	if err := validateJID(to); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("message text must not be empty")
	}
	warning, err := send(to, text)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"status": "sent", "to": to}
	if warning != "" {
		res["warning"] = warning
	}
	return res, nil
}
