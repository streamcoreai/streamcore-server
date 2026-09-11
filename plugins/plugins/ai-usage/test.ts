import { test } from "node:test";
import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

import { buildAgent, buildCard, buildSpeech, untilReset } from "./card";
import {
  CLAUDE,
  CODEX,
  collectClaude,
  findRateLimits,
  hasNumbers,
  missing,
  normalizeLiveRateLimits,
  readClaudeUsageFile,
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

test("card is one panel per agent, inside the panel's own caps", () => {
  const card = buildCard(
    [
      reading({
        agent: CLAUDE,
        fiveHourPercent: 62,
        weeklyPercent: 17,
        fiveHourResetsAt: NOW / 1000 + 7800,
        weeklyResetsAt: NOW / 1000 + 100800,
        ageMinutes: 2,
      }),
      reading({
        agent: CODEX,
        fiveHourPercent: 16,
        weeklyPercent: 16,
        fiveHourResetsAt: NOW / 1000 + 14520,
        ageMinutes: 5,
      }),
    ],
    NOW,
  );

  assert.equal(card.layout, "usage");
  assert.equal(card.title, "AI Usage");
  assert.equal(card.agents.length, 2);
  assert.equal(card.agents[0].name, "Claude Code");
  assert.equal(card.agents[0].note, "as of 11:58");
  assert.deepEqual(card.agents[0].gauges, [
    { label: "5H", percent: 62, reset: "@14:10" },
    { label: "7D", percent: 17, reset: "@Sep 7" },
  ]);
  // Codex's weekly window carried no reset time, so the gauge shows none
  // rather than inventing one.
  assert.deepEqual(card.agents[1].gauges[1], { label: "7D", percent: 16, reset: "" });

  for (const agent of card.agents) {
    assert.ok(agent.name.length <= 18, agent.name);
    assert.ok(agent.note.length <= 14, agent.note);
    assert.ok(agent.gauges.length <= 3, `${agent.gauges.length} gauges`);
    for (const gauge of agent.gauges) {
      assert.ok(gauge.label.length <= 3, gauge.label);
      assert.ok(gauge.reset.length <= 8, gauge.reset);
      assert.ok(gauge.percent === null || (gauge.percent >= 0 && gauge.percent <= 100));
      // The note4c turns anything non-ASCII into a question mark, and it
      // collapses whitespace runs before rendering.
      for (const text of [agent.name, agent.note, gauge.label, gauge.reset]) {
        assert.ok(/^[\x20-\x7e]*$/.test(text), text);
        assert.ok(!text.includes("  "), text);
      }
    }
  }
});

test("an agent says what it does not know instead of showing zero", () => {
  const partial = buildAgent(
    reading({ agent: CODEX, fiveHourPercent: null, weeklyPercent: 16, ageMinutes: 90 }),
    NOW,
  );
  assert.equal(partial.note, "as of 10:30");
  assert.equal(partial.gauges[0].percent, null);
  assert.equal(partial.gauges[1].percent, 16);

  // An agent with nothing at all still gets a panel, so the card says which
  // one went quiet rather than dropping it.
  const absent = buildAgent(missing(CLAUDE), NOW);
  assert.equal(absent.name, "Claude Code");
  assert.equal(absent.note, "no recent runs");
  assert.deepEqual(absent.gauges, []);
});

test("speech gives both windows in full, used and left, with the resets", () => {
  const spoken = buildSpeech(
    [
      reading({
        agent: CLAUDE,
        fiveHourPercent: 62,
        weeklyPercent: 17,
        fiveHourResetsAt: NOW / 1000 + 7800,
        weeklyResetsAt: NOW / 1000 + 100800,
        ageMinutes: 2,
      }),
      reading({
        agent: CODEX,
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
    /Claude Code has used 62 percent of its five-hour limit, 38 percent left, resetting at 14:10, in 2 hours 10 minutes, and 17 percent of its week, 83 percent left, resetting on Sep 7, in 1 day\. Read at 11:58\./,
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
  const spoken = buildSpeech([missing(CLAUDE), missing(CODEX)], NOW);
  assert.equal(
    spoken,
    "I couldn't find any recent Claude Code usage on this machine. " +
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

  assert.deepEqual(readClaudeSnapshot(path.join(dir, "nope"), NOW, 30), missing(CLAUDE));

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

  assert.deepEqual(readCodexSessions(path.join(root, "nope"), NOW, 30, 8), missing(CODEX));
});

test("the live Codex answer maps onto the same two windows as its logs", () => {
  // Exactly what `codex app-server` returns for account/rateLimits/read.
  const result = {
    rateLimits: {
      limitId: "codex",
      primary: { usedPercent: 0, windowDurationMins: 300, resetsAt: 1789141035 },
      secondary: { usedPercent: 8, windowDurationMins: 10080, resetsAt: 1789454135 },
      planType: "plus",
    },
  };

  const limits = normalizeLiveRateLimits(result);
  assert.ok(limits);
  const reading = readingFromRateLimits(limits, NOW / 1000, NOW, 30);
  assert.equal(reading.fiveHourPercent, 0);
  assert.equal(reading.weeklyPercent, 8);
  assert.equal(reading.fiveHourResetsAt, 1789141035);
  assert.equal(reading.weeklyResetsAt, 1789454135);
  // Asked for just now, so nothing about it is old.
  assert.equal(reading.ageMinutes, 0);
  assert.equal(reading.stale, false);

  // Zero is a real answer and must survive as one.
  assert.equal(hasNumbers(reading), true);
});

test("live windows are read by length, not by which slot they arrive in", () => {
  const swapped = normalizeLiveRateLimits({
    rateLimits: {
      primary: { usedPercent: 8, windowDurationMins: 10080, resetsAt: 200 },
      secondary: { usedPercent: 62, windowDurationMins: 300, resetsAt: 100 },
    },
  });
  assert.ok(swapped);
  const reading = readingFromRateLimits(swapped, NOW / 1000, NOW, 30);
  assert.equal(reading.fiveHourPercent, 62);
  assert.equal(reading.weeklyPercent, 8);
});

test("a response without rate limits is not mistaken for zero usage", () => {
  assert.equal(normalizeLiveRateLimits(null), null);
  assert.equal(normalizeLiveRateLimits({}), null);
  assert.equal(normalizeLiveRateLimits({ rateLimits: {} }), null);
  assert.equal(normalizeLiveRateLimits({ rateLimits: { primary: null, secondary: null } }), null);
});

test("the claude-hud snapshot is read, aged, and flagged once it is old", () => {
  const dir = tempDir();
  const file = path.join(dir, "usage-snapshot.json");
  const takenAt = new Date(NOW - 4 * 60_000).toISOString();
  fs.writeFileSync(
    file,
    JSON.stringify({
      updated_at: takenAt,
      five_hour: { used_percentage: 62, resets_at: "2026-09-06T14:10:00.000Z" },
      seven_day: { used_percentage: 17, resets_at: "2026-09-07T19:00:00.000Z" },
    }),
  );

  const fresh = readClaudeUsageFile(file, NOW, 30);
  assert.equal(fresh.agent, CLAUDE);
  assert.equal(fresh.fiveHourPercent, 62);
  assert.equal(fresh.weeklyPercent, 17);
  assert.equal(fresh.fiveHourResetsAt, Date.parse("2026-09-06T14:10:00.000Z") / 1000);
  assert.equal(fresh.ageMinutes, 4);
  assert.equal(fresh.stale, false);

  // The same file a week later is still read — and no longer trusted.
  const later = readClaudeUsageFile(file, NOW + 7 * 86_400_000, 30);
  assert.equal(later.fiveHourPercent, 62);
  assert.equal(later.stale, true);

  fs.rmSync(dir, { recursive: true, force: true });
});

test("a missing or shapeless usage file falls through to the older source", () => {
  const dir = tempDir();
  assert.equal(hasNumbers(readClaudeUsageFile(path.join(dir, "nope.json"), NOW, 30)), false);

  const junk = path.join(dir, "junk.json");
  fs.writeFileSync(junk, JSON.stringify({ updated_at: "2026-09-11T00:00:00Z" }));
  assert.equal(hasNumbers(readClaudeUsageFile(junk, NOW, 30)), false);

  // With no usage file at all, the legacy statusline snapshot answers.
  const legacy = path.join(dir, "eink");
  fs.mkdirSync(legacy);
  fs.writeFileSync(
    path.join(legacy, "s1.json"),
    JSON.stringify({
      timestamp: new Date(NOW - 60_000).toISOString(),
      usage: { fiveHour: 40, sevenDay: 9 },
    }),
  );
  const reading = collectClaude(
    { claude_usage_file: path.join(dir, "nope.json"), claude_snapshot_dir: legacy },
    NOW,
    30,
  );
  assert.equal(reading.fiveHourPercent, 40);

  fs.rmSync(dir, { recursive: true, force: true });
});

test("a stale reading says so on the card instead of looking current", () => {
  const old = buildAgent(
    reading({ agent: CODEX, fiveHourPercent: 16, weeklyPercent: 16, ageMinutes: 9 * 24 * 60, stale: true }),
    NOW,
  );
  assert.equal(old.note, "stale Aug 28");

  const now = buildAgent(
    reading({ agent: CODEX, fiveHourPercent: 16, weeklyPercent: 16, ageMinutes: 2 }),
    NOW,
  );
  assert.equal(now.note, "as of 11:58");
});

test("a separately metered model gets its own gauge and its own sentence", () => {
  const dir = tempDir();
  const file = path.join(dir, "usage-snapshot.json");
  fs.writeFileSync(
    file,
    JSON.stringify({
      updated_at: new Date(NOW - 60_000).toISOString(),
      five_hour: { used_percentage: 26, resets_at: "2026-09-06T14:20:00Z" },
      seven_day: { used_percentage: 52, resets_at: "2026-09-13T19:00:00Z" },
      model_scoped: [{ display_name: "Fable", used_percentage: 16, resets_at: "2026-09-13T19:00:00Z" }],
    }),
  );

  const reading = readClaudeUsageFile(file, NOW, 30);
  assert.deepEqual(reading.scoped, [
    { label: "Fable", percent: 16, resetsAt: Date.parse("2026-09-13T19:00:00Z") / 1000 },
  ]);

  const agent = buildAgent(reading, NOW);
  assert.deepEqual(
    agent.gauges.map((g) => [g.label, g.percent]),
    [["5H", 26], ["7D", 52], ["FABLE", 16]],
  );
  // Five characters, because a model name is not "FAB".
  assert.ok(agent.gauges.every((g) => g.label.length <= 5));

  assert.match(buildSpeech([reading], NOW), /Its Fable week is 16 percent used, 84 percent left/);

  fs.rmSync(dir, { recursive: true, force: true });
});

test("a plan with no model-scoped window is unchanged", () => {
  const plain = reading({ agent: CODEX, fiveHourPercent: 0, weeklyPercent: 8, ageMinutes: 1 });
  assert.deepEqual(buildAgent(plain, NOW).gauges.map((g) => g.label), ["5H", "7D"]);
});
