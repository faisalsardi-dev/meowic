// client.go is routing only: parse os.Args, hand off to the right
// actions/ function, print via Structure. No whatsmeow imports, no
// business logic.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"meowic/actions"
)

const usage = `usage: meowic <command> [args]

commands:
  doctor
  get-newsletter-info <newsletter-jid>
  list-newsletter-messages <newsletter-jid> [limit]
  get-group-info <group-jid>
  get-person-info <person-jid>
  list-chats [limit]
  list-messages <chat-jid> [limit]
  send-message <jid> <message>`

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		return Structure(nil, errors.New(usage))
	}
	cmd, args := os.Args[1], os.Args[2:]

	m, err := OpenMeow(ctx)
	if err != nil {
		return Structure(nil, err)
	}
	defer m.Close()

	switch cmd {
	case "doctor":
		return Structure(HealthReport(m))
	case "get-newsletter-info":
		return Structure(actions.GetNewsletterInfo(args, m.GetNewsletterInfo))
	case "list-newsletter-messages":
		return Structure(actions.ListNewsletterMessages(args, m.ListNewsletterMessages))
	case "get-group-info":
		return Structure(actions.GetGroupInfo(args, m.GetGroupInfo))
	case "get-person-info":
		return Structure(actions.GetPersonInfo(args, m.GetPersonInfo))
	case "list-chats":
		return Structure(actions.ListChats(args, m.ListChats))
	case "list-messages":
		return Structure(actions.ListMessages(args, m.ListMessages))
	case "send-message":
		return Structure(actions.SendMessage(args, m.SendText))
	default:
		return Structure(nil, fmt.Errorf("unknown command %q\n%s", cmd, usage))
	}
}
