# meowic — Customization Reference

This document is the **menu**. It exists so that, months from now, it's clear
*why* the shipped tool looks the way it does — and so a different build can be
assembled without re-deriving the whole design. Two things are on the menu:

1. **The whatsmeow actions you can choose from** (Section 1) — the full universe
   of capability, of which meowic exposes only a selected subset.
2. **The custom `logic/` rules you can bake in** (Section 2) — hardcoded,
   compile-time restrictions on the commands you *do* ship (e.g. auto-block
   foreign numbers, work-hours-only, disallow non-people).

---

## 1. The Full Action Universe

Everything this tool can possibly do comes from one library: `whatsmeow`
(`go.mau.fi/whatsmeow`). Nothing is implemented outside of what that
library exposes — there is no custom protocol work, no reimplementation.
Below is the public API surface, organized by category (excluding ~150
methods under `DangerousInternalClient`, which are raw, unexported protocol
internals not meant for normal use). Treat this as an illustrative snapshot,
not a line-by-line audit of the current whatsmeow release.

```
AUTH / CONNECTION
  Connect()
  Disconnect()
  GetQRChannel()
  PairPhone()
  Logout()
  IsConnected()
  IsLoggedIn()
  WaitForConnection()

SENDING
  SendMessage()            <- core send, used for text/media/replies/reactions/polls
  BuildEdit()
  BuildReaction()
  BuildRevoke()
  BuildPollCreation()
  BuildPollVote()

RECEIVING (passive, via event handler)
  AddEventHandler()
  AddEventHandlerWithSuccessStatus()
  RemoveEventHandler()

MEDIA
  Upload()
  UploadReader()
  Download()
  DownloadToFile()
  DownloadThumbnail()
  DownloadMediaWithPath()
  DeleteMedia()

CONTACTS / USERS
  GetUserInfo()
  IsOnWhatsApp()
  GetBusinessProfile()
  GetContactQRLink()
  ResolveContactQRLink()
  GetProfilePictureInfo()

GROUPS
  CreateGroup()
  LeaveGroup()
  GetGroupInfo()
  GetJoinedGroups()
  SetGroupName()
  SetGroupDescription()
  SetGroupPhoto()
  SetGroupTopic()
  SetGroupAnnounce()
  SetGroupLocked()
  UpdateGroupParticipants()
  GetGroupInviteLink()
  JoinGroupWithLink()
  GetGroupRequestParticipants()
  UpdateGroupRequestParticipants()

NEWSLETTERS / CHANNELS
  CreateNewsletter()
  FollowNewsletter()
  UnfollowNewsletter()
  GetNewsletterInfo()
  GetNewsletterMessages()

PRESENCE / STATUS
  SendPresence()
  SendChatPresence()        <- typing indicators
  SubscribePresence()
  SetStatusMessage()

PRIVACY / SETTINGS
  GetPrivacySettings()
  SetPrivacySetting()
  GetBlocklist()
  UpdateBlocklist()
  SetDisappearingTimer()
  MarkRead()

CALLS
  RejectCall()               <- only inbound rejection; no outbound calling implemented

---
MINIMAL SUBSET LIKELY NEEDED FOR A TYPICAL CLI:
  Connect() / PairPhone()       -> auth
  AddEventHandler()             -> receive messages
  SendMessage()                 -> send text
  GetUserInfo() / IsOnWhatsApp()-> contact lookup
  GetGroupInfo() / GetJoinedGroups() -> read-only group info
```

Every command this tool ships is a deliberate *choice* mapped onto a
subset of the above. The library exposes all of it; this tool exposes
only what's selected below.

---

## 2. Customizability Layers

There are two independent layers of customization in this design:

**Layer 1 — Command selection.** Simply choosing which of the actions
above get a CLI command at all. An action with no command wired to it
cannot be called, by anyone, under any circumstance — this is the
strongest form of restriction, since there's no flag to flip or
config to misconfigure.

**Layer 2 — Rule-based restriction on a shipped command.** For commands
that *are* wired up, additional hardcoded rules can be baked directly
into the code path, independent of any config file or environment
variable. This is for cases where you want the command to broadly work,
but with specific, non-negotiable exceptions.

> **A note on the MCP wrapper.** `mcp/` is effectively a third selection
> point: it chooses which of the binary's commands to surface as MCP tools.
> But the binary is the real boundary — a command that exists in the binary
> is still runnable from the CLI even if the wrapper doesn't expose it. Only
> Layer 1 (no command at all) is absolute.

### The variant shipped in this project
- `send-message` is enabled, always on — no feature flag gating it.
- `edit` was considered, then removed entirely (Layer 1) — not worth the
  added surface for a tool used this lightly.
- Media/file-sending was considered and rejected outright (Layer 1) —
  this was the single biggest risk identified during review of
  comparable projects (`lharries/whatsapp-mcp`'s `send_file`, and
  `whatsapp-cli`'s `send --image`, both accept arbitrary local file
  paths with no confinement). No file-path parameter exists anywhere in
  this codebase as a result.
- **Layer 2 rule**: `send-message` enforces an **allowlist** in
  `logic/sending_messages_only_to_people.go`. `CheckSend` permits only
  individual JIDs — `@s.whatsapp.net` and `@lid` — and refuses everything
  else (groups `@g.us`, channels `@newsletter`, bare numbers, and any future
  address type) with `message sending is only allowed to people`. An
  allowlist can't silently miss a "bad" suffix the way a denylist can.
  Changing it requires editing this file and recompiling — a deliberately
  high-friction action, never a flag.

> **Caveat — `Logout()` and connection-lifecycle actions are deliberately
> absent from `actions/`.** Looking at the Section 1 list, `Logout()`,
> `Disconnect()`, and `PairPhone()` all live under AUTH/CONNECTION, and
> it may look like an oversight that none of them became a CLI command.
> This is intentional, not a gap: unlinking the device (`Logout()`) is a
> destructive, account-level action, and the WhatsApp phone app's own
> Linked Devices screen is treated as the single source of truth for it
> — the CLI should never be able to unlink itself. `Disconnect()` exists
> only as internal plumbing inside `meow.go` (called on process shutdown
> signals like Ctrl+C), never as something an LLM can invoke on demand.
> `PairPhone()` is the first-time pairing method (a linking code typed
> into the phone, chosen over QR scanning) — it is internal plumbing in
> `meow.go`'s pairing flow, never a CLI command, so there is still only
> one pairing path and nothing extra exposed. If a future variant
> needs any of these exposed, treat it the same as any other Layer 1
> decision: a deliberate, individually-considered choice, not a default.

### Other variants (not shipped, illustrative only)

**A. "Work hours only" bot** — a variant aimed at a small-business use
case. `send-message` stays enabled (Layer 1), but a Layer 2 rule rejects any
send attempt outside 9am–6pm in the account owner's local timezone,
regardless of who's asking or why — preventing an LLM (or anyone) from
firing off messages at 3am.

**B. "VIP contacts only" assistant** — a variant for someone who only
wants an LLM replying to a short list of close contacts, never anyone
else. `send-message` is enabled (Layer 1), and a Layer 2 rule checks the
destination JID against an allowlist file (`vip_contacts.json`) —
the inverse of a denylist. If the JID isn't on the list, the send is
refused, no exceptions, no override flag.

**C. "Read-only" observer** — a variant that can see everything but touch
nothing. `send-message` is removed entirely (Layer 1): with no write
command wired up, sending isn't reachable by anyone, through the CLI or
the MCP, no matter what the LLM asks. The tool becomes pure observation —
the strongest restriction there is, because there's no rule to get wrong.

**D. "Rate-limited sender"** — a variant that trusts the agent to send but
not to *spam*. `send-message` stays enabled (Layer 1), and a Layer 2 rule
refuses once more than N messages have gone out in a rolling window (say,
10 per hour, tracked in the local store). A runaway or hijacked agent is
throttled regardless of recipient, and the cap can only be changed by
editing and recompiling.

### Worked example — Layer 2 restriction: geography-based block

A Layer 2 rule some might want: on top of the individuals-only allowlist,
**also block sending to any number that isn't a Saudi Arabia number
(country code +966).**

```go
// logic/sending_messages_only_to_people.go (or a sibling file in logic/,
// e.g. logic/saudi_only.go, following the same pattern)

const allowedCountryCode = "966"

func CheckSend(to string) error {
    // Base allowlist: individuals only (@s.whatsapp.net or @lid).
    if !strings.HasSuffix(to, "@s.whatsapp.net") && !strings.HasSuffix(to, "@lid") {
        return errors.New("message sending is only allowed to people")
    }

    // Extra rule: phone-number individuals must be Saudi (+966).
    if strings.HasSuffix(to, "@s.whatsapp.net") {
        number := strings.TrimSuffix(to, "@s.whatsapp.net")
        if !strings.HasPrefix(number, allowedCountryCode) {
            return fmt.Errorf("refusing to send: %s is not a %s (+%s) number",
                to, "Saudi Arabia", allowedCountryCode)
        }
    }

    return nil
}
```

This is the same *mechanism* as the shipped rule — a hardcoded, compile-time
check inside the one choke point every send must pass through, called from
`send_message.go` before `meow.go` ever touches the network. The only thing
that changes between variants is which condition the rule checks. This is the
intended pattern for any future restriction: add a condition in `logic/`, not a
new flag or config option, if the rule should be non-negotiable rather than
toggleable.

---

*This document is a reference snapshot at time of writing. If the
shipped command list in the main README diverges from Section 1 above,
the README is authoritative — update this file to match.*
