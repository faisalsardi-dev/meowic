package actions

import (
	"errors"
	"regexp"
	"strconv"
)

// ListNewsletterMessages fetches recent messages from a newsletter (channel).
func ListNewsletterMessages(args []string, fetch func(jid string, count int) (any, error)) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("usage: list-newsletter-messages <newsletter-jid> [limit]")
	}
	if err := validateJID(args[0]); err != nil {
		return nil, err
	}
	count, err := optionalLimit(args, 1, 50)
	if err != nil {
		return nil, err
	}
	return fetch(args[0], count)
}

// jidPattern accepts a JID iff the server is one of the 11 WhatsApp servers
// whatsmeow knows (see types/jid.go constants; hardcoded here because actions/
// stays free of whatsmeow imports) and the user part is at least one character
// containing no whitespace and no extra "@". The user part is otherwise
// unconstrained — "status@broadcast" and hyphenated legacy groups are valid —
// so a real JID is never wrongly rejected, while anything with an unknown
// server (123@foo.bar) fails HERE, locally, instead of leaking to the network
// as a confusing server error. Deciding WHICH valid JIDs may be acted on is
// policy (logic/), never this generic format gate.
var jidPattern = regexp.MustCompile(
	`^[^@\s]+@(s\.whatsapp\.net|lid|g\.us|newsletter|broadcast|c\.us|bot|msgr|interop|hosted|hosted\.lid)$`)

func validateJID(jid string) error {
	if !jidPattern.MatchString(jid) {
		return errors.New("invalid JID format")
	}
	return nil
}

// optionalLimit parses args[idx] as a positive integer, defaulting when absent.
func optionalLimit(args []string, idx, def int) (int, error) {
	if len(args) <= idx {
		return def, nil
	}
	n, err := strconv.Atoi(args[idx])
	if err != nil || n < 1 {
		return 0, errors.New("limit must be a positive integer")
	}
	return n, nil
}
