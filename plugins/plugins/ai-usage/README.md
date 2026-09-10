# AI Usage Plugin

**English** | [简体中文](./README.zh-CN.md)

Answers "how much AI have I used" out loud, and puts a two-column meter on the
caller's screen: Claude Code down one side, Codex down the other, with the
five-hour window and the week on each.

```text
AI USAGE
══════════════════════════════════════
 CLAUDE            │ CODEX
 as of 06:11       │ as of 06:08
 5H 62% used       │ 5H 16% used
 ======----        │ ==--------
 38% left @14:10   │ 84% left @10:53
 7D 17% used       │ 7D 16% used
 ==--------        │ ==--------
 83% left @Sep 7   │ 84% left @Sep 11
```

Say *"give me an update on my AI usage"* and the model calls `ai.usage`. The
tool speaks every number it has and emits one `display.card` v1 packet on the
`split` layout.

> **The `split` layout is new.** The [zectrix-note4c](../../../../examples/zectrix-note4c)
> firmware in this repository renders it, but a device still running an older
> build rejects the card as an unknown layout and keeps whatever was on screen.
> Reflash before expecting the panel to change.

## Where the numbers come from

Neither tool offers an API for its own rate limits, so both readings are
scavenged from files they already write. Nothing leaves the machine and no
credentials are read.

| Agent | Source | Notes |
| ----- | ------ | ----- |
| Claude Code | `~/.claude/plugins/claude-hud/eink-snapshots/*.json` | Claude Code hands its plan percentages to the **statusline on stdin** and nowhere else. Something has to tee them to disk; the snapshot written by a claude-hud statusline wrapper is what this reads. Freshest file wins, since the limits are per account |
| Codex | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` | Codex embeds `rate_limits` in its `token_count` events. The reader descends the date tree in reverse and tails the newest logs until one yields a count |

Both sources go stale the moment their tool stops running, so each column opens
with when its own numbers were taken (`as of 06:11`, or `as of Sep 4` once that
is no longer today). One stale source never casts doubt on the other. Times are
absolute rather than relative because the panel may sit unrefreshed for an
hour: `as of 06:11` stays true on the screen, `2m ago` does not.

A window the files did not answer reads `no data` rather than `0%`, and an
agent with nothing at all says so in place of its rows. The spoken line adds
how long ago a stale reading was taken, which the column has no room for.

## Configuration

Off by default, because the paths only exist on a machine where these tools
actually run.

```toml
[plugins.config."ai.usage"]
enabled = true

# All optional; these are the defaults.
claude_snapshot_dir = "~/.claude/plugins/claude-hud/eink-snapshots"
codex_sessions_dir  = "~/.codex/sessions"
stale_after_minutes = 30
codex_scan_files    = 8
```

Point `claude_snapshot_dir` at your own directory if your statusline writes the
snapshot somewhere else. The reader wants a JSON file shaped like:

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

The `split` layout: a title, then two columns of short lines either side of a
divider. Three constraints shaped it:

- **Two columns of 17 characters, 7 lines each.** That is what a 400 px panel
  fits at 10 px per character while staying readable across a room. The caps
  live in `note4c-logic/src/display_card.rs` and the plugin mirrors them.
- **Bars are `=`, `-` and `.`, not block glyphs.** The note4c maps every
  non-ASCII character to `?` before rendering, and `#`, `*` and brackets are
  rejected as markup by the sanitizer the display projector applies.
- **No column relies on multiple spaces.** Card parsers collapse every run of
  whitespace to a single space, so anything padded out to a fixed column
  arrives one space wide. The divider does the aligning instead.

Seven rows is the whole budget, so the reset time rides on the row with the
percentage left (`38% left @14:10`) rather than taking one of its own. The
five-hour window gets a clock time and the weekly one a date, since `@16:00`
tells you nothing about a reset six days out.

A blank line in a column is a spacer: the panel spends a row on it and draws
nothing. This plugin has no rows to spare for one, but the layout supports it.

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
| `card.ts` | Bars, the v1 card, and the spoken line |
| `test.ts` | `npx tsx --test test.ts` |
