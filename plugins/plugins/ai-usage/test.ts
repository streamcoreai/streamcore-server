import { test } from "node:test";
import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { bar, buildCard, buildColumn, buildSpeech, untilReset } from "./card";
import {
  findRateLimits,
  missing,
  newestRolloutFiles,
  readClaudeSnapshot,
  readCodexSessions,
  readingFromRateLimits,
  toEpochSeconds,
  type Reading,
} from "./sources";

const NOW = Date.parse("2026-09-06T12:00:00Z");

function reading(overrides: Partial<Reading> & { agent: string }): Reading {
  const base = { ...missing(overrides.agent), ...overrides };
  if (base.sampledAt === 0 && base.ageMinutes !== null) {
    base.sampledAt = NOW / 1000 - base.ageMinutes * 60;
  }
  return base;
}

function tempDir(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), "ai-usage-"));
}

test("bar fills proportionally and marks the unknown case apart", () => {
  assert.equal(bar(0), "----------");
  assert.equal(bar(62), "======----");
  assert.equal(bar(100), "==========");
  assert.equal(bar(140), "==========");
  assert.equal(bar(null), "..........");
});

test("card is one column per agent, inside the panel's own caps", () => {
  const card = buildCard(
    [
      reading({
        agent: "Claude",
        fiveHourPercent: 62,
        weeklyPercent: 17,
        fiveHourResetsAt: NOW / 1000 + 7800,
        weeklyResetsAt: NOW / 1000 + 100800,
        ageMinutes: 2,
      }),
      reading({
        agent: "Codex",
        fiveHourPercent: 16,
        weeklyPercent: 16,
        fiveHourResetsAt: NOW / 1000 + 14520,
        ageMinutes: 5,
      }),
    ],
    NOW,
  );

  assert.equal(card.layout, "split");
  assert.equal(card.title, "AI Usage");
  assert.equal(card.columns.length, 2);
  assert.equal(card.columns[0].heading, "Claude");
  assert.deepEqual(card.columns[0].lines, [
    "as of 11:58",
    "5H 62% used",
    "======----",
    "38% left @14:10",
    "7D 17% used",
    "==--------",
    "83% left @Sep 7",
  ]);
  // Codex's weekly window carried no reset time, so the row stops early
  // rather than inventing one.
  assert.equal(card.columns[1].lines[6], "84% left");

  for (const column of card.columns) {
    assert.ok(column.lines.length <= 7, `${column.lines.length} lines`);
    assert.ok(column.heading.length <= 17, column.heading);
    for (const line of column.lines) {
      assert.ok(line.length <= 17, line);
      // The projector's sanitizer rejects these outright, and the note4c turns
      // anything non-ASCII into a question mark.
      assert.ok(!/[#*`_<>\[\]]/.test(line), line);
      assert.ok(/^[\x20-\x7e]*$/.test(line), line);
      // The note4c collapses whitespace runs before rendering, so a row that
      // relies on multiple spaces to line up will not line up on the device.
      assert.ok(!line.includes("  "), line);
    }
  }
});

test("a column says what it does not know instead of showing zero", () => {
  const partial = buildColumn(
    reading({ agent: "Codex", fiveHourPercent: null, weeklyPercent: 16, ageMinutes: 90 }),
    NOW,
  );
  assert.equal(partial.heading, "Codex");
  assert.deepEqual(partial.lines.slice(0, 4), ["as of 10:30", "5H no data", "..........", ""]);

  const absent = buildColumn(missing("Claude"), NOW);
  assert.equal(absent.heading, "Claude");
  assert.deepEqual(absent.lines, ["no recent runs", "on this machine"]);
});

test("speech gives both windows in full, used and left, with the resets", () => {
  const spoken = buildSpeech(
    [
      reading({
        agent: "Claude",
        fiveHourPercent: 62,
        weeklyPercent: 17,
        fiveHourResetsAt: NOW / 1000 + 7800,
        weeklyResetsAt: NOW / 1000 + 100800,
        ageMinutes: 2,
      }),
      reading({
        agent: "Codex",
        fiveHourPercent: 16,
        weeklyPercent: null,
        fiveHourResetsAt: NOW / 1000 + 600,
        ageMinutes: 130,
        stale: true,
      }),
    ],
    NOW,
  );

  assert.match(
    spoken,
    /Claude has used 62 percent of its five-hour limit, 38 percent left, resetting at 14:10, in 2 hours 10 minutes, and 17 percent of its week, 83 percent left, resetting on Sep 7, in 1 day\. Read at 11:58\./,
  );
  // An unknown window is said as unknown, not folded into a number.
  assert.match(
    spoken,
    /Codex has used 16 percent of its five-hour limit, 84 percent left, resetting at 12:10, in 10 minutes, and its week is unknown/,
  );
  // A stale reading says both when it was taken and how long ago that was.
  assert.match(spoken, /Read at 09:50, 2 hours ago\./);
});

test("speech says so when a source produced nothing", () => {
  const spoken = buildSpeech([missing("Claude"), missing("Codex")], NOW);
  assert.equal(
    spoken,
    "I couldn't find any recent Claude usage on this machine. " +
      "I couldn't find any recent Codex usage on this machine.",
  );
});

test("untilReset stays quiet about a window that already rolled over", () => {
  assert.equal(untilReset(NOW / 1000 - 60, NOW), "");
  assert.equal(untilReset(null, NOW), "");
  assert.equal(untilReset(NOW / 1000 + 1800, NOW), "30m");
  assert.equal(untilReset(NOW / 1000 + 3 * 86400, NOW), "3d");
});

test("toEpochSeconds accepts both shapes the sources use", () => {
  assert.equal(toEpochSeconds(1788562402), 1788562402);
  assert.equal(toEpochSeconds(1788562402000), 1788562402);
  assert.equal(toEpochSeconds("2026-09-06T12:00:00Z"), NOW / 1000);
  assert.equal(toEpochSeconds("nonsense"), null);
  assert.equal(toEpochSeconds(0), null);
});

test("findRateLimits reaches the object wherever Codex nests it", () => {
  const line = JSON.parse(
    '{"timestamp":"2026-09-05T06:15:45Z","type":"event_msg","payload":{"type":"token_count",' +
      '"rate_limits":{"primary":{"used_percent":16.0,"window_minutes":300,"resets_at":1788562402}}}}',
  );
  assert.equal(findRateLimits(line)?.primary?.used_percent, 16);
  assert.equal(findRateLimits({ a: { b: { c: 1 } } }), null);
});

test("the shorter window is the rolling one regardless of primary/secondary", () => {
  const swapped = readingFromRateLimits(
    {
      primary: { used_percent: 80, window_minutes: 10080, resets_at: 1788562402 },
      secondary: { used_percent: 16, window_minutes: 300, resets_at: 1788500000 },
    },
    NOW / 1000,
    NOW,
    30,
  );
  assert.equal(swapped.fiveHourPercent, 16);
  assert.equal(swapped.weeklyPercent, 80);
  assert.equal(swapped.stale, false);
});

test("claude snapshot reader takes the freshest session and flags its age", () => {
  const dir = tempDir();
  fs.writeFileSync(
    path.join(dir, "old.json"),
    JSON.stringify({
      timestamp: "2026-09-06T06:00:00Z",
      usage: { fiveHour: 99, sevenDay: 99 },
    }),
  );
  fs.writeFileSync(
    path.join(dir, "new.json"),
    JSON.stringify({
      timestamp: "2026-09-06T11:50:00Z",
      usage: {
        fiveHour: 62,
        sevenDay: 17,
        fiveHourResetAt: "2026-09-06T14:00:00Z",
        sevenDayResetAt: "2026-09-08T00:00:00Z",
      },
    }),
  );
  // A snapshot without a usage object is a session Claude Code never reported
  // limits for, not a zero.
  fs.writeFileSync(path.join(dir, "empty.json"), JSON.stringify({ timestamp: "2026-09-06T11:59:00Z" }));

  const result = readClaudeSnapshot(dir, NOW, 30);
  assert.equal(result.fiveHourPercent, 62);
  assert.equal(result.weeklyPercent, 17);
  assert.equal(result.ageMinutes, 10);
  assert.equal(result.stale, false);
  assert.equal(result.fiveHourResetsAt, Date.parse("2026-09-06T14:00:00Z") / 1000);

  assert.deepEqual(readClaudeSnapshot(path.join(dir, "nope"), NOW, 30), missing("Claude"));

  const staleResult = readClaudeSnapshot(dir, NOW + 3 * 3600 * 1000, 30);
  assert.equal(staleResult.stale, true);
});

test("codex reader walks back to the newest session that actually has a count", () => {
  const root = tempDir();
  const day = (y: string, m: string, d: string) => {
    const dir = path.join(root, y, m, d);
    fs.mkdirSync(dir, { recursive: true });
    return dir;
  };

  fs.writeFileSync(
    path.join(day("2025", "10", "25"), "rollout-2025-10-25T02-22-06-aaa.jsonl"),
    JSON.stringify({ timestamp: "2025-10-25T02:22:06Z", payload: { rate_limits: { primary: { used_percent: 90, window_minutes: 300 } } } }) + "\n",
  );
  // The newest session opened but never called the model.
  fs.writeFileSync(
    path.join(day("2026", "09", "05"), "rollout-2026-09-05T06-15-45-ccc.jsonl"),
    JSON.stringify({ timestamp: "2026-09-05T06:15:45Z", payload: { type: "user_message" } }) + "\n",
  );
  fs.writeFileSync(
    path.join(day("2026", "09", "04"), "rollout-2026-09-04T10-00-00-bbb.jsonl"),
    JSON.stringify({ timestamp: "2026-09-06T11:30:00Z", payload: { type: "user_message" } }) +
      "\n" +
      JSON.stringify({
        timestamp: "2026-09-06T11:45:00Z",
        payload: {
          type: "token_count",
          rate_limits: {
            primary: { used_percent: 16.4, window_minutes: 300, resets_at: NOW / 1000 + 3600 },
            secondary: { used_percent: 42, window_minutes: 10080, resets_at: NOW / 1000 + 86400 },
          },
        },
      }) +
      "\n",
  );

  const files = newestRolloutFiles(root, 8);
  assert.ok(files[0].includes("2026/09/05"), files[0]);

  const result = readCodexSessions(root, NOW, 30, 8);
  assert.equal(result.fiveHourPercent, 16);
  assert.equal(result.weeklyPercent, 42);
  assert.equal(result.ageMinutes, 15);
  assert.equal(result.stale, false);

  assert.deepEqual(readCodexSessions(path.join(root, "nope"), NOW, 30, 8), missing("Codex"));
});
