#!/bin/sh
# meowic verification matrix — proves the shipped safety guarantees against the
# COMPILED BINARY. Rather than poking package internals, every check drills the
# binary through its real front door (argv -> client -> actions -> logic -> JSON
# out), so it verifies exactly what ships.
#
# Run from anywhere after building:
#     CGO_ENABLED=1 go build -o meowic . && ./scripts/verify.sh
#
# Every offline check is safe to run unpaired: validateJID (actions/) and
# logic.CheckSend both reject BEFORE any network I/O, and list reads only touch
# the local mirror. Nothing here connects, sends, or needs a paired session.
#
# HISTORY LESSON (why the inputs below are what they are): a 15-digit JID bug
# once survived manual testing because the check used a truncated 13-digit
# example. This matrix pins the REAL shapes — 18-digit groups, hyphen legacy
# groups, @lid, status@broadcast — so that can't recur.

cd "$(dirname "$0")/.." || exit 1
[ -x ./meowic ] || { echo "no ./meowic binary — build first: CGO_ENABLED=1 go build -o meowic ."; exit 1; }

pass=0
fail=0

# expect <name> <required-output-substring> <meowic-arg>...
expect() {
	name=$1
	want=$2
	shift 2
	out=$(./meowic "$@" 2>/dev/null)
	case $out in
	*"$want"*)
		pass=$((pass + 1))
		echo "PASS  $name"
		;;
	*)
		fail=$((fail + 1))
		echo "FAIL  $name"
		echo "      want substring: $want"
		echo "      got: $(printf '%s' "$out" | tr '\n' ' ' | cut -c1-160)"
		;;
	esac
}

nl='
'

echo "== format gate: real JIDs must be VALID input (mirror reads, offline) =="
expect "person @s.whatsapp.net"      '"ok": true' list-messages 966512345678@s.whatsapp.net 1
expect "hidden person @lid"          '"ok": true' list-messages 231142506668036@lid 1
expect "18-digit group @g.us"        '"ok": true' list-messages 120363021234567890@g.us 1
expect "legacy hyphen group @g.us"   '"ok": true' list-messages 12345-1600000000@g.us 1
expect "channel @newsletter"         '"ok": true' list-messages 120363021234567890@newsletter 1
expect "status feed status@broadcast" '"ok": true' list-messages status@broadcast 1
expect "legacy user @c.us"           '"ok": true' list-messages 966512345678@c.us 1
expect "bot @bot"                    '"ok": true' list-messages 13135550002@bot 1
expect "messenger interop @msgr"     '"ok": true' list-messages 123456@msgr 1
expect "EU DMA interop @interop"     '"ok": true' list-messages 123456@interop 1
expect "hosted @hosted"              '"ok": true' list-messages 123456@hosted 1
expect "hosted lid @hosted.lid"      '"ok": true' list-messages 123456@hosted.lid 1

echo "== format gate: garbage must fail LOCALLY (never reach the network) =="
expect "unknown server"              'invalid JID format' list-messages 123@foo.bar 1
expect "near-miss server"            'invalid JID format' list-messages 123@whatsapp.net 1
expect "suffix overrun"              'invalid JID format' list-messages 123@s.whatsapp.netx 1
expect "empty user part"             'invalid JID format' list-messages @g.us 1
expect "whitespace in user part"     'invalid JID format' list-messages 'a b@g.us' 1
expect "extra @"                     'invalid JID format' list-messages a@b@g.us 1
expect "bare number, no server"      'invalid JID format' list-messages 966512345678 1
expect "uppercase server"            'invalid JID format' list-messages 123@G.US 1
expect "trailing newline"            'invalid JID format' list-messages "123@g.us${nl}" 1
expect "empty string"                'invalid JID format' list-messages '' 1
expect "pure garbage"                'invalid JID format' list-messages '###not a jid###' 1

echo "== policy gate: CheckSend refuses non-people (pre-network, nothing sent) =="
expect "send to 18-digit group"      'only allowed to people' send-message 120363021234567890@g.us x
expect "send to channel"             'only allowed to people' send-message 120363021234567890@newsletter x
# @lid is now an allowed send target (individuals only: @s.whatsapp.net + @lid).
# A real @lid + text send would actually transmit if online, so it can't be an
# offline row — proving the allow-path is a live check (see checklist below).
# Here we only pin that a @lid recipient still clears the shape gate and is then
# stopped by the empty-text guard, all BEFORE any network I/O.
expect "send @lid blank text refused" 'must not be empty' send-message 231142506668036@lid '   '

echo "== arg contracts =="
expect "send: 1 arg rejected"        'usage: send-message' send-message 966512345678@s.whatsapp.net
expect "send: 3 args rejected"       'usage: send-message' send-message 966512345678@s.whatsapp.net hello there
expect "send: blank text rejected"   'must not be empty' send-message 966512345678@s.whatsapp.net '   '
expect "limit: non-numeric rejected" 'limit must be a positive integer' list-chats abc
expect "limit: zero rejected"        'limit must be a positive integer' list-chats 0
expect "limit: over-max rejected"    'limit must be at most 200' list-chats 100000000
expect "limit: at-max accepted"      '"ok": true'                list-chats 200

echo "== runtime read-only switch (MEOWIC_READONLY) — tightens only, offline-safe =="
# The read-only gate is the FIRST thing in SendText, so an otherwise-allowed
# individual send is refused before any network I/O — safe to assert offline.
# Reads never reach SendText, so they must keep working with the flag set.
export MEOWIC_READONLY=1
expect "read-only: allowed send refused" 'read-only mode' send-message 966512345678@s.whatsapp.net hi
expect "read-only: reads still work"     '"ok": true'     list-chats 1
unset MEOWIC_READONLY

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1

# ---------------------------------------------------------------------------
# LIVE CHECKLIST — needs a paired session; run by hand when the change
# touches send, mirror, or rendering behavior. (These are the parts the
# offline matrix cannot prove; verify against real traffic before considering
# a build complete.)
#
#   1. Allow-path + own-send mirror (BOTH individual servers now allowed):
#        ./meowic send-message <own-jid>@s.whatsapp.net "verify test"
#        ./meowic list-messages <own-jid>@s.whatsapp.net 1   # must show it
#        ./meowic send-message <known-jid>@lid "verify lid"  # @lid now allowed
#        ./meowic list-messages <known-jid>@lid 1            # must show it
#   2. renderText live (media tags), via any followed channel:
#        ./meowic list-newsletter-messages <channel-jid> 5
#        -> an image post must read: [image: <size>] <caption>
#   3. After receiving real media in any chat:
#        ./meowic list-messages <chat-jid> 5
#        -> [image: 340KB] caption / [voice note: 0m42s, 180KB] /
#           [document: name.pdf, 1.1MB] / [video: 1m20s, 4.2MB] / [sticker]
#   4. LID contact resolution:
#        ./meowic get-person-info <known @lid>   # is_contact: true
#   5. Channels listed AND mirrored (channel posts are stored, with
#      list-newsletter-messages kept as the live backup):
#        ./meowic list-chats 10                  # channel row present
#        ./meowic list-messages <channel-jid> 5  # NEW posts now appear here
#        ./meowic list-newsletter-messages <channel-jid> 5  # live backup path
#   6. Reduced group / newsletter info emits ONLY vetted fields (no raw
#      whatsmeow structs — no owner/participant phone numbers or LID
#      cross-links, no invite code, no picture CDN URLs):
#        ./meowic get-group-info <group-jid>
#        -> jid, name, topic, created, is_announce/locked/ephemeral/community,
#           participant_count, participants[].{jid,is_admin,is_super_admin}
#        ./meowic get-newsletter-info <channel-jid>
#        -> jid, name, description, subscriber_count, verified, created,
#           role, muted  (NO "invite" code, NO picture URLs)
#   7. Unsupported-message marker (M4): from another phone, send yourself a
#      poll or a location, then:
#        ./meowic list-messages <that-chat-jid> 5
#        -> the poll/location shows as "[unsupported message]" (a visible row),
#           while a reaction leaves NO new row.
#   7b. Revoke / edit reconciliation (M1): from another phone, send yourself a
#      text, confirm it mirrors, then delete-for-everyone / edit it:
#        ./meowic list-messages <that-chat-jid> 5   # original text present
#        (delete-for-everyone on the phone)
#        ./meowic list-messages <that-chat-jid> 5   # row is GONE
#        (edit a different message on the phone)
#        ./meowic list-messages <that-chat-jid> 5   # row shows the NEW text
#   7c. Own-send mirror-failure warning (L4): hard to force naturally; a send
#      whose mirror write fails must still return status:"sent" WITH a
#      "warning" field (never an error) — inspect the send-message envelope.
#   8. Non-interactive pairing guard (H2): against an UNPAIRED data dir
#      (temporarily move store/data/session.db aside), with stdin a PIPE —
#      the shape the MCP wrapper uses (Node execFile pipes the child's stdin):
#        printf '' | ./meowic doctor
#        -> clean error "stdin is not a terminal … run ./meowic doctor in a
#           terminal to pair"; it must NOT hang or request a real code.
#           (Restore session.db afterward.)
# ---------------------------------------------------------------------------
