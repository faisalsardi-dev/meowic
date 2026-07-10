package commands

import "errors"

// GetGroupInfo fetches metadata for a group the account is a member of.
// Read-only: being able to inspect a group does not enable sending to it.
func GetGroupInfo(args []string, fetch func(jid string) (any, error)) (any, error) {
	if len(args) != 1 {
		return nil, errors.New("usage: get-group-info <group-jid>")
	}
	return fetch(args[0])
}
