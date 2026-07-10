package commands

import (
	"errors"
	"strings"

	"github.com/faisalsardi-dev/meowic/logic"
)

// SendPeople is the only write command: it sends a text message to an
// individual contact. There is no media, file, or attachment parameter
// of any kind — the absence is deliberate and must stay that way.
//
// logic.CheckSend runs BEFORE the injected send function, so the
// group-send restriction is enforced before any network I/O happens.
func SendPeople(args []string, send func(to, text string) error) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("usage: send-people <jid|number> <message...>")
	}
	to := args[0]
	text := strings.Join(args[1:], " ")
	if err := logic.CheckSend(to); err != nil {
		return nil, err
	}
	if err := send(to, text); err != nil {
		return nil, err
	}
	return map[string]any{"status": "sent", "to": to}, nil
}
