// Package logic holds hardcoded, non-configurable send restrictions — the
// customization layer. Rules here are compile-time only: changing them
// requires editing this file and recompiling. No flag, config file, or
// environment variable can alter them. The actions/ package stays generic
// and applies none of this; a different build can reuse actions/ unchanged
// and supply its own logic/.
package logic

import (
	"errors"
	"strings"
)

// CheckSend must be called before any send reaches the network. This build
// messages individuals only, enforced as an ALLOWLIST: the recipient must be
// an individual — either a phone JID ("@s.whatsapp.net") or a hidden-user JID
// ("@lid", the same person addressed by a privacy-hidden number). Everything
// else — groups (@g.us), channels (@newsletter), bare numbers, and any future
// address type — is refused by default. An allowlist can't silently miss a
// "bad" suffix the way a denylist can.
func CheckSend(to string) error {
	if !strings.HasSuffix(to, "@s.whatsapp.net") && !strings.HasSuffix(to, "@lid") {
		return errors.New("message sending is only allowed to people")
	}
	return nil
}
