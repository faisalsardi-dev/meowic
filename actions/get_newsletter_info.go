package actions

import "errors"

// GetNewsletterInfo fetches metadata for a newsletter (channel).
func GetNewsletterInfo(args []string, fetch func(jid string) (any, error)) (any, error) {
	if len(args) != 1 {
		return nil, errors.New("usage: get-newsletter-info <newsletter-jid>")
	}
	if err := validateJID(args[0]); err != nil {
		return nil, err
	}
	return fetch(args[0])
}
