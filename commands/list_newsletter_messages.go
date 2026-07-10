package commands

import (
	"errors"
	"strconv"
)

// ListNewsletterMessages fetches recent messages from a newsletter (channel).
func ListNewsletterMessages(args []string, fetch func(jid string, count int) (any, error)) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("usage: list-newsletter-messages <newsletter-jid> [count]")
	}
	count, err := optionalLimit(args, 1, 50)
	if err != nil {
		return nil, err
	}
	return fetch(args[0], count)
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
