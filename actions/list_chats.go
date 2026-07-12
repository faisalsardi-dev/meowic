package actions

import (
	"meowic/store"
)

// ListChats reads the locally synced chat list. No network access.
func ListChats(args []string, list func(limit int) ([]store.Chat, error)) (any, error) {
	limit, err := optionalLimit(args, 0, 50)
	if err != nil {
		return nil, err
	}
	return list(limit)
}
