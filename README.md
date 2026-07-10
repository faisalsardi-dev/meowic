# meowic

WhatsApp CLI built on [whatsmeow](https://github.com/tulir/whatsmeow). Every command prints JSON on stdout.

## Install

Requires Go 1.24+ and a C compiler (sqlite driver uses CGO).

```sh
git clone https://github.com/faisalsardi-dev/meowic
cd meowic
CGO_ENABLED=1 go build -o meowic .
```

## First run — pairing

Run from the repo root (data is stored in `./store/data/`):

```sh
./meowic doctor
```

With no session, you are prompted for the account's phone number (international format, digits only) and an 8-character linking code is printed to the terminal. Enter it on your phone: **WhatsApp → Settings → Linked devices → Link a device → Link with phone number instead**. The session is saved to `store/data/session.db`; message history syncs into `store/data/messages.db` while connected.

## Usage

```sh
./meowic doctor                                        # health check: connection, login, store status
./meowic list-chats [limit]                            # synced chat list (local, offline)
./meowic list-messages <chat-jid> [limit]              # synced messages for a chat (local, offline)
./meowic get-group-info <group-jid>                    # e.g. 1203630212345@g.us
./meowic get-newsletter-info <newsletter-jid>          # e.g. 12036302xxxxx@newsletter
./meowic list-newsletter-messages <newsletter-jid> [count]
./meowic send-people <jid|number> <message...>         # e.g. ./meowic send-people 966512345678 hello there
```

`send-people` is text-only and refuses group JIDs (`…@g.us`) — this is hardcoded, see `CUSTOMIZE.md`.
