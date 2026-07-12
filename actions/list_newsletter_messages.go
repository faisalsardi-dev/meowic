package actions

import (
	"errors"
	"regexp"
	"strconv"
)

// ListNewsletterMessages fetches recent messages from a newsletter (channel).
func ListNewsletterMessages(args []string, fetch func(jid string, count int) (any, error)) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("usage: list-newsletter-messages <newsletter-jid> [count]")
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

// jidPattern is a SHAPE check, not a server whitelist: a numeric user part
// (optionally "digits-digits" for legacy groups) followed by "@" and a
// lowercase server. It accepts every WhatsApp address type — @s.whatsapp.net,
// @g.us, @newsletter, @lid, and any server whatsmeow adds later — while still
// rejecting garbage (###, abc@x) and bare numbers. Deciding WHICH valid JIDs
// may be acted on is policy (logic/), never this generic format gate.
var jidPattern = regexp.MustCompile(`^[0-9]+(-[0-9]+)?@[a-z.]+$`)

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
