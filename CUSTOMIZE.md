# whatsapp-cli — Customization Reference

This document is an archived reference for how this tool's capabilities are
selected and restricted. It exists so that, months from now, it's clear
*why* the shipped version looks the way it does, and how to reshape it for
a different use case without re-deriving the whole design from scratch.

---

## 1. The Full Action Universe

Everything this tool can possibly do comes from one library: `whatsmeow`
(`go.mau.fi/whatsmeow`). Nothing is implemented outside of what that
library exposes — there is no custom protocol work, no reimplementation.
Below is the complete public API surface, organized by category
(excluding ~150 methods under `DangerousInternalClient`, which are raw,
unexported protocol internals not meant for normal use).

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
  Download() / DownloadToFile() -> media in (quarantine folder)
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

### The variant shipped in this project
- `send` is enabled, always on — no feature flag gating it.
- `edit` was considered, then removed entirely (Layer 1) — not worth the
  added surface for a tool used this lightly.
- Media/file-sending was considered and rejected outright (Layer 1) —
  this was the single biggest risk identified during review of
  comparable projects (`lharries/whatsapp-mcp`'s `send_file`, and
  `whatsapp-cli`'s `send --image`, both accept arbitrary local file
  paths with no confinement). No file-path parameter exists anywhere in
  this codebase as a result.
- **Layer 2 rule**: `send` refuses any JID ending in `@g.us` (WhatsApp's
  suffix for groups). This is a hardcoded check inside the send path
  itself (`logic/disallow_sending_to_groups.go`), not a flag — sending
  to groups requires editing and recompiling the source, a deliberately
  high-friction action.

> **Caveat — `Logout()` and connection-lifecycle actions are deliberately
> absent from `commands/`.** Looking at the Section 1 list, `Logout()`,
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

### Two other believable variants (not shipped, illustrative only)

**A. "Work hours only" bot** — a variant aimed at a small-business use
case. `send` stays enabled (Layer 1), but a Layer 2 rule rejects any
send attempt outside 9am–6pm in the account owner's local timezone,
regardless of who's asking or why — preventing an LLM (or anyone) from
firing off messages at 3am.

**B. "VIP contacts only" assistant** — a variant for someone who only
wants an LLM replying to a short list of close contacts, never anyone
else. `send` is enabled (Layer 1), and a Layer 2 rule checks the
destination JID against an allowlist file (`vip_contacts.json`) —
the inverse of a denylist. If the JID isn't on the list, the send is
refused, no exceptions, no override flag.

### Worked example — Layer 2 restriction: geography-based block

A believable Layer 2 rule some might want: **block sending to any
number that isn't a Saudi Arabia number (country code +966).**

```go
// logic/disallow_sending_to_groups.go (or a sibling file, e.g.
// logic/saudi_only.go, following the same pattern)

const allowedCountryCode = "966"

func CheckSend(to string) error {
    if strings.HasSuffix(to, "@g.us") {
        return errors.New("sending to groups is disabled by design")
    }

    // JIDs for individuals look like "9665XXXXXXXX@s.whatsapp.net"
    number := strings.TrimSuffix(to, "@s.whatsapp.net")
    if !strings.HasPrefix(number, allowedCountryCode) {
        return fmt.Errorf("refusing to send: %s is not a %s (+%s) number",
            to, "Saudi Arabia", allowedCountryCode)
    }

    return nil
}
```

This is exactly the same *mechanism* as the group-block rule — a
hardcoded, compile-time check inside the one choke point every send
must pass through, called from `send_people.go` before `meow.go` ever
touches the network. The only thing that changes between variants is
which condition the rule checks. This is the intended pattern for any
future restriction: add a condition in `logic/`, not a new flag or
config option, if the rule should be non-negotiable rather than
toggleable.

---

*This document is a reference snapshot at time of writing. If the
shipped command list in the main README diverges from Section 1 above,
the README is authoritative — update this file to match.*
