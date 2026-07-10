package commands

import (
	"errors"

	"github.com/faisalsardi-dev/meowic/store"
)

// ListMessages reads locally synced messages for one chat. No network access.
func ListMessages(args []string, list func(chatJID string, limit int) ([]store.Message, error)) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("usage: list-messages <chat-jid> [limit]")
	}
	limit, err := optionalLimit(args, 1, 50)
	if err != nil {
		return nil, err
	}
	return list(args[0], limit)
}
