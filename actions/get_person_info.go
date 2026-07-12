package actions

import "errors"

// GetPersonInfo fetches identity info for one person JID. Read-only: it is
// the lookup companion to send-message (e.g. figuring out who a @lid chat
// belongs to), not a way to message anyone.
func GetPersonInfo(args []string, fetch func(jid string) (any, error)) (any, error) {
	if len(args) != 1 {
		return nil, errors.New("usage: get-person-info <person-jid>")
	}
	if err := validateJID(args[0]); err != nil {
		return nil, err
	}
	return fetch(args[0])
}
