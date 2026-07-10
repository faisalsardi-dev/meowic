// Package logic holds hardcoded, non-configurable send restrictions.
// Rules here are compile-time only: changing them requires editing this
// file and recompiling. No flag, config file, or environment variable
// can alter them.
package logic

import (
	"errors"
	"strings"
)

// CheckSend must be called before any send reaches the network.
// It refuses any JID ending in "@g.us" (WhatsApp's suffix for groups).
func CheckSend(to string) error {
	if strings.HasSuffix(to, "@g.us") {
		return errors.New("sending to groups is disabled by design")
	}
	return nil
}
