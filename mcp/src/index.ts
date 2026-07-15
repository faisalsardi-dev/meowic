#!/usr/bin/env node
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { mkdirSync, chmodSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import { z } from "zod";

// This wrapper lives at <repo>/mcp/ and execs the compiled binary at the repo
// root — resolved RELATIVE to this file so a clone works anywhere, with no
// machine-specific path. Built layout: <repo>/mcp/dist/index.js, so two levels
// up is the repo root, where `meowic` and ./store/data/ live. The wrapper NEVER
// imports the Go packages — execing the binary is the load-bearing rule, so the
// binary's own policy layer (logic/CheckSend, validateJID) can't be bypassed.
const MEOWIC_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const MEOWIC_BIN = resolve(MEOWIC_DIR, "meowic");
// The binary spends up to 15s in WaitForConnection and THEN does the actual
// network fetch, so the wrapper budget must exceed the binary's internal connect
// allowance — a 15s budget would kill slow-but-healthy live calls mid-fetch.
// (Pairing can take minutes, but the binary's non-TTY guard refuses it here.)
const TIMEOUT_MS = 45_000;
// Node's execFile caps stdout at 1 MiB by default; a list at the max limit (200)
// of long messages can exceed that, which would kill a SUCCESSFUL read and
// mislabel it as a failure. Give it generous headroom.
const MAX_BUFFER = 64 * 1024 * 1024;

// The child runs with an empty env EXCEPT the read-only switch: forwarding
// MEOWIC_READONLY lets the binary's send gate apply to MCP-driven sends too, so
// setting it on the MCP server makes the agent observe-only. The send-message
// tool stays registered on purpose — a blocked send then returns "read-only
// mode" and lands in the tool_calls log, which is the point of the switch.
const CHILD_ENV: NodeJS.ProcessEnv = process.env.MEOWIC_READONLY
  ? { MEOWIC_READONLY: process.env.MEOWIC_READONLY }
  : {};

// execFile — NOT exec. execFile never spawns a shell, so there is no string to
// inject into; args are passed as a real argv array to execve(). cwd is pinned
// to the repo root so the binary finds ./store/data/{session,messages}.db.
// `failure` captures wrapper-level problems the binary can't report itself —
// chiefly the 15s timeout, which kills the process before it prints anything.
// Single-flight: two meowic processes connecting at once would link to WhatsApp
// as the SAME device simultaneously (stream-replaced / desync) and contend on the
// sqlite files. Chaining every exec through one promise guarantees at most one is
// in flight, no matter how many tool calls the client fires concurrently.
let execChain: Promise<unknown> = Promise.resolve();

function runMeowic(args: string[]): Promise<{ stdout: string; failure: string | null }> {
  const result = execChain.then(() => execMeowic(args));
  // Keep the chain alive across a failure so one bad call can't stall the rest.
  execChain = result.catch(() => {});
  return result;
}

function execMeowic(args: string[]): Promise<{ stdout: string; failure: string | null }> {
  return new Promise((resolve) => {
    execFile(
      MEOWIC_BIN,
      args,
      { timeout: TIMEOUT_MS, maxBuffer: MAX_BUFFER, cwd: MEOWIC_DIR, env: CHILD_ENV },
      (error, stdout, stderr) => {
        let failure: string | null = null;
        if (error) {
          const e = error as NodeJS.ErrnoException & { killed?: boolean; signal?: string };
          // A normal command error still exits with a JSON envelope on stdout
          // (exit 1) — code set, not killed — so it is NOT a wrapper failure; the
          // envelope parse downstream wins. Only spawn/kill cases are failures,
          // classified distinctly so the tool_calls log tells the truth.
          if (e.code === "ERR_CHILD_PROCESS_STDOUT_MAXBUFFER") failure = "output too large";
          else if (e.killed) failure = "timeout";
          else if (e.code) failure = null;
          else failure = e.message;
        }
        // The binary writes diagnostics to stderr (pairing guidance, warnings).
        // When it produced no stdout envelope, fold the tail of stderr into the
        // failure so the reason isn't lost (the wrapper otherwise discards stderr).
        if (failure && !stdout.trim() && stderr && stderr.trim()) {
          const tail = stderr.trim().split("\n").slice(-3).join(" ");
          failure = `${failure}: ${tail}`;
        }
        resolve({ stdout, failure });
      }
    );
  });
}

// --- tool-call log: one table recording every call the LLM makes THROUGH the
// MCP wrapper (CLI runs of the binary bypass this by design). Kept in its own
// sqlite next to the other DBs; the Go binary is never touched by it. node:sqlite
// is built into Node 22.5+ — if unavailable, logging silently no-ops so a clone
// on older Node still works. ---
let insertCall: ReturnType<DatabaseSync["prepare"]> | null = null;
try {
  const dataDir = resolve(MEOWIC_DIR, "store", "data");
  mkdirSync(dataDir, { recursive: true });
  const dbPath = resolve(dataDir, "mcp_calls.db");
  const db = new DatabaseSync(dbPath);
  // The log holds jids and message bodies — lock it to the owner like the Go
  // side does for session.db / messages.db. Best-effort (ignored on failure).
  try {
    chmodSync(dbPath, 0o600);
  } catch {
    /* non-POSIX fs or race — the 0700 data dir still gates access */
  }
  db.exec(`CREATE TABLE IF NOT EXISTS tool_calls (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    tool      TEXT NOT NULL,
    jid       TEXT,
    message   TEXT,
    limit_n   INTEGER,
    ok        INTEGER NOT NULL,
    error     TEXT,
    timestamp TEXT NOT NULL
  );`);
  insertCall = db.prepare(
    `INSERT INTO tool_calls (tool, jid, message, limit_n, ok, error, timestamp)
     VALUES (?, ?, ?, ?, ?, ?, ?)`
  );
} catch (err) {
  console.error("[meowic-mcp] tool-call logging disabled:", (err as Error).message);
}

// Derive the outcome from the binary's {ok, data, error} envelope. A parsed
// envelope always wins (covers ok:true AND the binary's own ok:false errors,
// e.g. a policy-refused send); only when nothing parses do we fall back to the
// wrapper-level failure (timeout) or a generic "no output".
function outcome(stdout: string, failure: string | null): { ok: number; error: string | null } {
  try {
    const env = JSON.parse(stdout);
    if (typeof env.ok === "boolean") {
      return { ok: env.ok ? 1 : 0, error: env.ok ? null : env.error ? String(env.error) : "error" };
    }
  } catch {
    /* not JSON — fall through */
  }
  return { ok: 0, error: failure ?? (stdout.trim() ? "unparseable output" : "no output") };
}

function logCall(
  tool: string,
  fields: { jid?: unknown; message?: unknown; limit?: unknown },
  stdout: string,
  failure: string | null
): void {
  if (!insertCall) return;
  const { ok, error } = outcome(stdout, failure);
  try {
    insertCall.run(
      tool,
      fields.jid != null ? String(fields.jid) : null,
      fields.message != null ? String(fields.message) : null,
      fields.limit != null ? Number(fields.limit) : null,
      ok,
      error,
      new Date().toISOString()
    );
  } catch (err) {
    console.error("[meowic-mcp] tool-call log write failed:", (err as Error).message);
  }
}

// A read tool takes an optional jid and/or limit and forwards them positionally
// (jid then limit) — the exact order meowic's actions/ layer expects.
function registerReadTool(
  server: McpServer,
  name: string,
  description: string,
  schema: Record<string, z.ZodTypeAny>
) {
  server.registerTool(
    name,
    { description, inputSchema: schema },
    async (params: Record<string, unknown>) => {
      const args: string[] = [name];
      if ("jid" in params && params.jid) args.push(String(params.jid));
      if ("limit" in params && params.limit != null) args.push(String(params.limit));
      const { stdout, failure } = await runMeowic(args);
      logCall(name, { jid: params.jid, limit: params.limit }, stdout, failure);
      return { content: [{ type: "text", text: stdout }] };
    }
  );
}

const server = new McpServer({ name: "meowic", version: "1.0.0" });

// --- read + status tools (7) ---
registerReadTool(server, "doctor", "Health check: connection, login, store status.", {});
registerReadTool(server, "list-chats", "Synced chat list (local, offline).", {
  limit: z.number().int().positive().optional(),
});
registerReadTool(
  server,
  "list-messages",
  "Synced messages for a chat (local, offline). Now includes channel/newsletter posts too.",
  { jid: z.string(), limit: z.number().int().positive().optional() }
);
registerReadTool(server, "get-group-info", "Group name, description, participants (live).", {
  jid: z.string(),
});
registerReadTool(server, "get-newsletter-info", "Channel/newsletter metadata (live).", {
  jid: z.string(),
});
registerReadTool(
  server,
  "list-newsletter-messages",
  "Channel/newsletter recent posts (live backup read path).",
  { jid: z.string(), limit: z.number().int().positive().optional() }
);
registerReadTool(
  server,
  "get-person-info",
  "Identity for a person JID: resolves @lid -> phone, contact status, verified name, profile-image presence (live + local).",
  { jid: z.string() }
);

// --- the one write tool (1) — extra-careful description ---
server.registerTool(
  "send-message",
  {
    description:
      "Send a plain text message to an individual. The recipient must be a FULL " +
      "JID (e.g. 966512345678@s.whatsapp.net, or a @lid JID) — bare numbers are " +
      "not accepted. Only individuals are valid targets: @s.whatsapp.net and " +
      "@lid. Groups (@g.us) and channels (@newsletter) are refused per rules, " +
      "before any network I/O. Sends immediately and cannot be recalled. Always " +
      "confirm the exact recipient and message text with the user before calling. " +
      "NOT idempotent: if a call times out or errors, do NOT automatically resend " +
      "— it may have already delivered. Verify with list-messages first. A success " +
      "may include a 'warning' field (e.g. mirror write failed); it still sent.",
    inputSchema: { jid: z.string(), text: z.string().min(1) },
  },
  async ({ jid, text }: { jid: string; text: string }) => {
    const { stdout, failure } = await runMeowic(["send-message", jid, text]);
    logCall("send-message", { jid, message: text }, stdout, failure);
    return { content: [{ type: "text", text: stdout }] };
  }
);

// docs://usage — based on scripts/mcpdocinital.txt, reconciled to current code
// (send-message full-JID, @lid sendable, channels mirrored, get-person-info).
server.registerResource(
  "usage",
  "docs://usage",
  { title: "meowic usage", mimeType: "text/plain" },
  async (uri) => ({
    contents: [
      {
        uri: uri.href,
        text: `meowic — WhatsApp CLI, wrapped as MCP tools. Every command prints a JSON
envelope ({"ok","data","error"}) on stdout.

8 commands (1 setup/status - 6 read - 1 write)
==============================================

- doctor
    Args: none
    Returns: health report — connected, logged_in, session_exists, store
             paths, chat/message counts, last_sync/last_send.

- list-chats
    Args: [limit]  (default 50)
    Returns: array of { jid, name, last_message_time }, newest first.

- list-messages
    Args: <chat-jid> (required), [limit] (default 50)
    Returns: array of { id, chat_jid, sender_jid, from_me, timestamp, text },
             newest first. Now includes channel/newsletter posts too.

- get-group-info
    Args: <group-jid> (exactly one)
    Returns: group metadata (live).

- get-newsletter-info
    Args: <newsletter-jid> (exactly one)
    Returns: newsletter metadata (live).

- list-newsletter-messages
    Args: <newsletter-jid> (required), [limit] (default 50)
    Returns: array of newsletter messages (live backup; posts are also mirrored
             into list-messages).

- get-person-info
    Args: <person-jid> (exactly one)
    Returns: { jid, found, lid, is_contact, status, verified_name, has_image }.
             found=false means WhatsApp returned no info for the JID (vs a real
             but blank profile). Resolves a @lid to its phone JID before the
             contact lookup.

- send-message
    Args: <jid> <message>  — a FULL JID and the text, exactly two arguments.
    Returns: { status: "sent", to }, plus an optional "warning" (see below).

notes:
- send-message takes a full JID only (no bare-number resolution). A person's
  number 966500000000 must be written as 966500000000@s.whatsapp.net.
- Sending is allowed ONLY to individuals: @s.whatsapp.net and @lid. Reads accept
  any valid JID.
- send-message is NOT idempotent — a fresh message ID is minted each call and
  WhatsApp does not dedupe. If a send times out or errors, do NOT auto-retry; it
  may have already delivered. Check list-messages for the recipient first.
- A successful send may carry a "warning" field (e.g. the local mirror write
  failed): the message still went out — treat it as sent, do not resend.
- Any optional [limit] must be a positive integer (default 50) and at most 200,
  otherwise the command errors ("limit must be a positive integer" / "at most 200").
- doctor is the overall check-up: one call reports connection, login, session,
  store paths, chat/message counts, and last sync/send — a snapshot of whether
  everything is healthy. It also doubles as a connection resync: running it
  connects, so new messages pull into the local mirror (and the first run
  triggers pairing).

A JID's suffix tells you its kind. All 11 known WhatsApp servers (per whatsmeow):
  - @s.whatsapp.net -> individual person, addressed by phone number (sendable)
  - @lid            -> individual person with a HIDDEN number ("Linked ID" privacy addressing; sendable)
  - @g.us           -> group chat (NOT sendable, refused per rules)
  - @newsletter     -> channel / newsletter (read live via list-newsletter-messages AND mirrored in list-messages; NOT sendable)
  - @broadcast      -> broadcast list or Status; "status@broadcast" is the Status/Stories feed
  - @c.us           -> legacy user server (pre-multidevice; some official/PSA accounts)
  - @bot            -> bot accounts, e.g. Meta AI
  - @msgr           -> Messenger interop (Meta cross-app w/ Facebook Messenger)     [uncommon; lower confidence]
  - @interop        -> third-party messaging interoperability (EU DMA cross-app)    [uncommon; lower confidence]
  - @hosted         -> hosted account (server/cloud-hosted; likely Business API)    [rare; lower confidence]
  - @hosted.lid     -> hosted account with hidden-number (LID) addressing           [rare; lower confidence]`,
      },
    ],
  })
);

async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("[meowic-mcp] ready on stdio"); // stderr only — stdout is JSON-RPC
}
main().catch((err) => {
  console.error("Fatal:", err);
  process.exit(1);
});
