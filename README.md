# xmpp-mcp

Minimal bidirectional MCP server that bridges XMPP to MCP clients. Listens for incoming XMPP messages (DMs and MUC) and pushes them to connected MCP clients as `notifications/message`. Exposes a single `send_message` tool.

**Status:** `0.0.1` — early, single-user, best-effort.

## Features

- **Incoming events** — DMs and group-chat (MUC) messages are forwarded to every active MCP session as `notifications/message`.
- **Outgoing tool** — one tool, `send_message(to, body)`, sends a chat message to any JID.
- **Whitelist filtering** — only senders/rooms listed in env vars are forwarded.
- **SCRAM-SHA-1-PLUS auth** — prefers channel binding over TLS 1.3; falls back to SCRAM-SHA-1 / PLAIN.
- **Auto-join MUCs** — joins every room in `XMPP_WHITELIST_MUCS` on connect.

## Requirements

- Go 1.22+
- An XMPP account on a server you control (Prosody tested; ejabberd/ejabberd should work but untested)
- Valid TLS certificate on the XMPP server (self-signed will fail; `mellium` verifies strictly)

## Build

```bash
make            # → bin/xmpp-mcp
make check      # fmt + vet + test
```

## Configuration

All configuration is via environment variables.

| Variable | Required | Description |
|----------|----------|-------------|
| `XMPP_JID` | ✅ | Bot account, bare JID (e.g. `elsa@example.com`) |
| `XMPP_PASSWORD` | ✅ | Password for the bot account |
| `XMPP_WHITELIST_USERS` | — | Comma-separated bare JIDs allowed to DM the bot |
| `XMPP_WHITELIST_MUCS` | — | Comma-separated bare room JIDs to join and listen on |
| `XMPP_NICK` | — | Nickname inside MUC rooms (default: `mcp-bot`) |
| `XMPP_DEBUG` | — | Any non-empty value enables verbose logging of forwarded messages |

Example `.env`:

```dotenv
XMPP_JID=alice@example.com
XMPP_PASSWORD=secret

XMPP_WHITELIST_USERS=bob@example.com
XMPP_WHITELIST_MUCS=general@muc.example.com

XMPP_NICK=Alice
```

## Running

```bash
set -a; source .env; set +a
./bin/xmpp-mcp
```

The server speaks MCP over stdio. Wire it into any MCP client (Claude Desktop, Cursor, etc.) with something like:

```json
{
  "mcpServers": {
    "xmpp": {
      "command": "/path/to/bin/xmpp-mcp",
      "env": {
        "XMPP_JID": "alice@example.com",
        "XMPP_PASSWORD": "secret",
        "XMPP_WHITELIST_USERS": "bob@example.com",
        "XMPP_WHITELIST_MUCS": "general@muc.example.com",
        "XMPP_NICK": "Alice"
      }
    }
  }
}
```

## Protocol

### Tool: `send_message`

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| `to` | string | ✅ | Recipient JID (bare or full) |
| `body` | string | ✅ | Message text |

Returns `"queued"` on success (the actual send is asynchronous).

### Notification: `notifications/message`

Sent to every registered MCP session when a whitelisted message arrives.

```json
{
  "method": "notifications/message",
  "params": {
    "level": "info",
    "data": "[general@muc.example.com] bob: hi",
    "from": "npopov",
    "room": "general@muc.example.com",
    "body": "hi"
  }
}
```

For DMs, `room` is empty and `from` is the sender's bare JID.

## Limitations (0.0.1)

- **Single account, single process.** No multi-tenancy, no shared state.
- **No reconnect loop.** If the TCP connection drops, the process exits. Run under `systemd`/`runit` with `Restart=always` for now.
- **No offline message replay.** Messages sent while the bot is offline are delivered once it connects (Prosody handles this), but the MCP client must already be attached to receive the notification. If the MCP session registers after the burst, those notifications are lost.
- **No MAM / history.** Only live messages are forwarded.
- **No OMEMO.** TLS to the server only; message bodies are plaintext on the server.
- **No presence management.** Always-online, no status handling.
- **No file/attachment support.** Only `<body>` text.

## Roadmap

- Reconnect loop with exponential backoff
- Optional MAM catch-up on connect
- Multi-room nick handling
- OMEMO (long-term)

## License

MIT
