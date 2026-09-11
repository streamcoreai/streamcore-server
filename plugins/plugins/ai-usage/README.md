# AI Usage Plugin

**English** | [简体中文](./README.zh-CN.md)

Answers "how much AI have I used" out loud, and puts a panel per agent on the
caller's screen: Claude Code above, Codex below, each with a gauge for the
five-hour window and one for the week.

```text
 AI USAGE                          14:32
┌────────────────────────────────────────┐
│ CLAUDE CODE                as of 06:11 │
│ 5H     74% left  ██████████░░░  @02:20 │
│ 7D     48% left  ███████░░░░░░  @Sun   │
│ FABLE  84% left  ████████████░  @Sun   │
└────────────────────────────────────────┘
┌────────────────────────────────────────┐
│ CODEX                      as of 06:08 │
│ 5H    100% left  █████████████  @06:07 │
│ 7D     92% left  ████████████░  @Sep15 │
└────────────────────────────────────────┘
```

The plugin sends what has been **used**; the panel draws what is **left** and
labels it. Claude Code's `/usage` says "26% used" and the Codex CLI says "100%
remaining", so an unlabelled percentage is a number the reader has to guess the
direction of.

Say *"give me an update on my AI usage"* and the model calls `ai.usage`. The
tool speaks every number it has and emits one `display.card` v1 packet on the
`usage` layout.

> **The `usage` layout is new.** The [zectrix-note4c](../../../../examples/zectrix-note4c)
> firmware in this repository renders it, but a device still running an older
> build rejects the card as an unknown layout and keeps whatever was on screen.
> Reflash before expecting the panel to change.

## Where the numbers come from

Neither tool publishes its limits to a file of its own, so each agent has a
live route and a scavenged one. The live route is tried first; the fallback is
kept for when it cannot answer, because an old number beats no number on a
panel — as long as it says it is old.

| Agent | Live | Fallback |
| ----- | ---- | -------- |
| Claude Code | `~/.claude/usage-snapshot.json`, written by a statusline wrapper | the older `eink-snapshots/*.json` |
| Codex | `codex app-server` → `account/rateLimits/read` | `rate_limits` scavenged from `~/.codex/sessions/**/rollout-*.jsonl` |

**Codex is genuinely live.** Its CLI runs a local app-server that answers
`account/rateLimits/read` with the current figures — it goes and asks rather
than reporting what some past session happened to see. Three lines of JSON-RPC
over stdio, about a second:

```text
-> {"id":1,"method":"initialize","params":{"clientInfo":{…}}}
-> {"method":"initialized","params":{}}
-> {"id":2,"method":"account/rateLimits/read","params":{}}
<- {"id":2,"result":{"rateLimits":{
     "primary":  {"usedPercent":0,"windowDurationMins":300,"resetsAt":1789141035},
     "secondary":{"usedPercent":8,"windowDurationMins":10080,"resetsAt":1789454135},
     "planType":"plus"}}}
```

Codex uses its own stored credentials for that; this plugin never reads or
forwards a token. The windows are matched by `windowDurationMins` rather than
by which slot they arrive in, because primary and secondary have swapped
before.

**Claude Code cannot be asked.** It hands its plan percentages to the
statusline on stdin and nowhere else — no file of its own, no local query, no
`claude usage` subcommand, and nothing in its session transcripts. So the
number is only as fresh as the last time a statusline rendered, and something
that sees that stdin has to tee it to disk. Two consequences worth knowing
before you trust the panel:

- **A statusline has to be configured**, with a wrapper that writes the
  snapshot (claude-hud does this on `display.externalUsageWritePath`).
- **Environments that do not render one produce nothing.** The VS Code
  extension is the trap: it never runs `statusLine`, so heavy daily use there
  leaves the snapshot untouched and the card can quietly show last week's
  reading. A terminal session — any session, any repo — feeds it again, and
  `refreshInterval` in the `statusLine` setting keeps an idle one ticking.

A snapshot writer should rewrite the file **only when the numbers move**, and
leave `updated_at` alone otherwise. Restamping an unchanged reading every
refresh makes an untouched number look current, which defeats the staleness
label below.

That second case is what `stale` exists for: a reading older than
`stale_after_minutes` is labelled `stale Sep 4` on the panel instead of
`as of 06:11`, and the spoken line adds how long ago it was taken. A source
that produced nothing at all says `no recent runs` rather than borrowing the
other agent's freshness.

Times are absolute rather than relative because the panel may sit unrefreshed
for an hour: `as of 06:11` stays true on the screen, `2m ago` does not.

A window a source did not answer sends `null` rather than `0`, which the panel
draws hatched instead of empty — an unread limit and an untouched one are not
the same thing.

## Configuration

Off by default, because the paths only exist on a machine where these tools
actually run.

```toml
[plugins.config."ai.usage"]
enabled = true

# All optional; these are the defaults.
claude_usage_file   = "~/.claude/usage-snapshot.json"           # the live one
claude_snapshot_dir = "~/.claude/plugins/claude-hud/eink-snapshots"
codex_live          = true       # ask the Codex CLI before reading its logs
codex_command       = "codex"    # if it is not on the server's PATH
codex_timeout_ms    = 6000       # the live query takes about a second
codex_sessions_dir  = "~/.codex/sessions"
codex_scan_files    = 8
stale_after_minutes = 30         # older than this is labelled stale on screen
```

`claude_usage_file` has to match wherever your statusline writes its snapshot.
claude-hud calls that setting `display.externalUsageWritePath`; any wrapper
that writes the same shape will do:

```json
{
  "updated_at": "2026-09-11T09:58:00.000Z",
  "five_hour": { "used_percentage": 62, "resets_at": "2026-09-11T14:10:00Z" },
  "seven_day": { "used_percentage": 17, "resets_at": "2026-09-15T19:00:00Z" }
}
```

Setting `codex_live = false` skips the CLI entirely and reads only the rollout
logs, which is what a server with no Codex installed wants.

Point `claude_snapshot_dir` at your own directory if your statusline writes the
older per-session shape somewhere else. The reader wants a JSON file shaped like:

```json
{
  "timestamp": "2026-09-06T11:50:00Z",
  "usage": {
    "fiveHour": 62,
    "sevenDay": 17,
    "fiveHourResetAt": "2026-09-06T14:00:00Z",
    "sevenDayResetAt": "2026-09-08T00:00:00Z"
  }
}
```

Files without a `usage` object are skipped, so a statusline that has not yet
seen a rate limit does not read as zero usage.

## The card

The `usage` layout: a title, then a panel per agent, each with a gauge per
window. The plugin sends numbers, not pictures — `{"label": "5H", "percent":
62, "reset": "@14:10"}` — and the client decides what 62 percent looks like. On
the note4c that is a filled bar; on a screen with more room it could be
anything.

The caps live in `note4c-logic/src/display_card.rs` and the plugin mirrors
them: three agents, a name of 18 characters, a note of 14, three gauges each
with a 3-character label and an 8-character reset stamp. They are what a 400 px
panel fits at 10 px per character while staying readable across a room.

Two details the shape does not show:

- **`percent` is nullable.** `null` means the source did not say, which the
  note4c draws as a hatched track. Sending `0` there would claim a limit that
  had not been touched.
- **A third gauge appears when the plan earns one.** `rate_limits.model_scoped`
  carries a window for a model metered apart from the rest — "Fable this week"
  in `/usage` — and it rides along under its own name rather than being folded
  into the weekly number it is not part of.
- **The reset stamp is already formatted**, because only the plugin knows the
  server's local time. The five-hour window gets a clock time and the weekly
  one a date, since `@16:00` tells you nothing about a reset six days out.

One thing to know before enabling this alongside `display-projector`. The
server passes a tool's emissions through verbatim and does not fill in the turn
number, so this plugin numbers its own cards in wall-clock seconds to guarantee
a card asked for *now* wins the client's latest-wins slot. Clients keep the
highest sequence they have seen for a session, so once a usage card has been
shown, the projector's cards — numbered from the conversation's own small turn
counter — stop winning for the rest of that session.

## Files

| File | Purpose |
| ---- | ------- |
| `plugin.yaml` | Manifest. Tool name, description the model reads, `enabled: false` |
| `index.ts` | Wiring: config at initialize, speak + emit on execute |
| `sources.ts` | Reading and normalizing both agents' files |
| `card.ts` | The v1 card, its gauges, and the spoken line |
| `test.ts` | `npx tsx --test test.ts` |
