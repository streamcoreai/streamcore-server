// Where the numbers come from. Neither agent exposes a query API for its own
// rate limits, so both readings are scavenged from files the tools already
// write while they run.

import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";

/** One agent's standing against its limits, as far as we can tell locally. */
export interface Reading {
  /** Leads each row on the card and each clause in the spoken line. */
  agent: string;
  fiveHourPercent: number | null;
  weeklyPercent: number | null;
  /** Epoch seconds, or null when the source did not say. */
  fiveHourResetsAt: number | null;
  weeklyResetsAt: number | null;
  /** How old the underlying sample is. null when nothing was found at all. */
  ageMinutes: number | null;
  /** When the sample was taken, epoch seconds. 0 when nothing was found. */
  sampledAt: number;
  /** Older than the configured threshold, so the numbers may have moved. */
  stale: boolean;
}

export interface Settings {
  claude_snapshot_dir?: string;
  codex_sessions_dir?: string;
  stale_after_minutes?: number;
  codex_scan_files?: number;
}

export const DEFAULT_CLAUDE_SNAPSHOT_DIR =
  "~/.claude/plugins/claude-hud/eink-snapshots";
export const DEFAULT_CODEX_SESSIONS_DIR = "~/.codex/sessions";
export const DEFAULT_STALE_AFTER_MINUTES = 30;
export const DEFAULT_CODEX_SCAN_FILES = 8;

/** Codex rollout logs run to megabytes; only the tail can hold the last count. */
const CODEX_TAIL_BYTES = 256 * 1024;
const CLAUDE_SNAPSHOT_LIMIT = 32;

export function expandHome(input: string): string {
  if (input === "~") return os.homedir();
  if (input.startsWith("~/")) return path.join(os.homedir(), input.slice(2));
  return input;
}

export function missing(agent: string): Reading {
  return {
    agent,
    fiveHourPercent: null,
    weeklyPercent: null,
    fiveHourResetsAt: null,
    weeklyResetsAt: null,
    ageMinutes: null,
    sampledAt: 0,
    stale: false,
  };
}

function clampPercent(value: unknown): number | null {
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  return Math.round(Math.min(100, Math.max(0, value)));
}

/** Codex writes epoch seconds, the Claude snapshot writes ISO-8601. */
export function toEpochSeconds(value: unknown): number | null {
  if (typeof value === "number" && Number.isFinite(value) && value > 0) {
    return value > 1e12 ? Math.round(value / 1000) : Math.round(value);
  }
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    if (!Number.isNaN(parsed)) return Math.round(parsed / 1000);
  }
  return null;
}

function ageMinutes(sampleEpochSeconds: number | null, nowMs: number): number | null {
  if (sampleEpochSeconds === null) return null;
  return Math.max(0, Math.round((nowMs / 1000 - sampleEpochSeconds) / 60));
}

// ── Claude Code ─────────────────────────────────────────────────────────

/**
 * Claude Code hands its plan percentages to the statusline on stdin and nowhere
 * else, so this reads the snapshot a statusline wrapper leaves behind rather
 * than any first-party file. Shape:
 *
 *   { "timestamp": "...", "usage": { "fiveHour": 62, "sevenDay": 17,
 *                                    "fiveHourResetAt": "...", ... } }
 *
 * A deployment whose statusline writes a different shape points
 * `claude_snapshot_dir` at its own directory; anything without a `usage` object
 * is skipped rather than guessed at.
 */
export function readClaudeSnapshot(dir: string, nowMs: number, staleAfterMinutes: number): Reading {
  const reading = missing("Claude");

  let entries: string[];
  try {
    entries = fs.readdirSync(dir).filter((name) => name.endsWith(".json"));
  } catch {
    return reading;
  }

  // Several sessions each keep their own snapshot. The freshest one is the
  // whole account's standing, since the limits are per account not per session.
  let best: { at: number; usage: Record<string, unknown> } | null = null;
  for (const name of entries.slice(0, CLAUDE_SNAPSHOT_LIMIT)) {
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(fs.readFileSync(path.join(dir, name), "utf8"));
    } catch {
      continue;
    }
    const usage = parsed.usage;
    if (!usage || typeof usage !== "object") continue;
    const at = toEpochSeconds(parsed.timestamp);
    if (at === null) continue;
    if (!best || at > best.at) best = { at, usage: usage as Record<string, unknown> };
  }
  if (!best) return reading;

  const age = ageMinutes(best.at, nowMs);
  return {
    agent: "Claude",
    fiveHourPercent: clampPercent(best.usage.fiveHour),
    weeklyPercent: clampPercent(best.usage.sevenDay),
    fiveHourResetsAt: toEpochSeconds(best.usage.fiveHourResetAt),
    weeklyResetsAt: toEpochSeconds(best.usage.sevenDayResetAt),
    ageMinutes: age,
    sampledAt: best.at,
    stale: age !== null && age > staleAfterMinutes,
  };
}

// ── Codex ───────────────────────────────────────────────────────────────

/** The `rate_limits` object Codex embeds in its `token_count` events. */
interface CodexRateLimits {
  primary?: { used_percent?: unknown; window_minutes?: unknown; resets_at?: unknown };
  secondary?: { used_percent?: unknown; window_minutes?: unknown; resets_at?: unknown };
}

/**
 * Pull the `rate_limits` object out of one parsed rollout line. Codex nests it
 * under `payload` today, but the key has moved before, so this walks rather
 * than hard-coding the path.
 */
export function findRateLimits(value: unknown, depth = 0): CodexRateLimits | null {
  if (depth > 6 || !value || typeof value !== "object") return null;
  const record = value as Record<string, unknown>;
  const direct = record.rate_limits;
  if (direct && typeof direct === "object") return direct as CodexRateLimits;
  for (const child of Object.values(record)) {
    const found = findRateLimits(child, depth + 1);
    if (found) return found;
  }
  return null;
}

/**
 * Turn one rollout line into a reading. Codex labels its two windows primary
 * and secondary, but which is which has not always held, so the shorter
 * `window_minutes` is taken as the rolling window and the longer as the week.
 */
export function readingFromRateLimits(
  limits: CodexRateLimits,
  sampleEpochSeconds: number | null,
  nowMs: number,
  staleAfterMinutes: number,
): Reading {
  const windows = [limits.primary, limits.secondary]
    .filter((w): w is NonNullable<typeof w> => !!w && typeof w === "object")
    .map((w) => ({
      minutes: typeof w.window_minutes === "number" ? w.window_minutes : Number.MAX_SAFE_INTEGER,
      percent: clampPercent(w.used_percent),
      resetsAt: toEpochSeconds(w.resets_at),
    }))
    .sort((a, b) => a.minutes - b.minutes);

  const rolling = windows[0];
  const weekly = windows[1];
  const age = ageMinutes(sampleEpochSeconds, nowMs);

  return {
    agent: "Codex",
    fiveHourPercent: rolling?.percent ?? null,
    weeklyPercent: weekly?.percent ?? null,
    fiveHourResetsAt: rolling?.resetsAt ?? null,
    weeklyResetsAt: weekly?.resetsAt ?? null,
    ageMinutes: age,
    sampledAt: sampleEpochSeconds ?? 0,
    stale: age !== null && age > staleAfterMinutes,
  };
}

/** Read the last `bytes` of a file as whole lines, oldest first. */
function tailLines(file: string, bytes: number): string[] {
  let handle: number | null = null;
  try {
    handle = fs.openSync(file, "r");
    const size = fs.fstatSync(handle).size;
    const start = Math.max(0, size - bytes);
    const length = size - start;
    if (length <= 0) return [];
    const buffer = Buffer.allocUnsafe(length);
    fs.readSync(handle, buffer, 0, length, start);
    const lines = buffer.toString("utf8").split("\n");
    // A non-zero offset almost certainly lands mid-record.
    if (start > 0) lines.shift();
    return lines;
  } catch {
    return [];
  } finally {
    if (handle !== null) {
      try {
        fs.closeSync(handle);
      } catch {
        /* already gone */
      }
    }
  }
}

/**
 * Collect the newest rollout logs without walking the whole tree. The sessions
 * directory is `YYYY/MM/DD/rollout-*.jsonl`, so descending each level in
 * reverse order reaches the newest files after reading a handful of directories
 * instead of every day Codex has ever run.
 */
export function newestRolloutFiles(root: string, limit: number): string[] {
  const descend = (dir: string): string[] => {
    try {
      return fs
        .readdirSync(dir, { withFileTypes: true })
        .filter((entry) => entry.isDirectory())
        .map((entry) => entry.name)
        .sort()
        .reverse();
    } catch {
      return [];
    }
  };

  const files: string[] = [];
  for (const year of descend(root)) {
    for (const month of descend(path.join(root, year))) {
      for (const day of descend(path.join(root, year, month))) {
        const dayDir = path.join(root, year, month, day);
        let dayFiles: string[];
        try {
          dayFiles = fs
            .readdirSync(dayDir)
            .filter((name) => name.startsWith("rollout-") && name.endsWith(".jsonl"))
            .sort()
            .reverse()
            .map((name) => path.join(dayDir, name));
        } catch {
          continue;
        }
        files.push(...dayFiles);
        if (files.length >= limit) return files.slice(0, limit);
      }
    }
  }
  return files.slice(0, limit);
}

export function readCodexSessions(
  dir: string,
  nowMs: number,
  staleAfterMinutes: number,
  scanFiles: number,
): Reading {
  // The newest session need not have called the model yet, so keep looking
  // back until a file yields a count.
  for (const file of newestRolloutFiles(dir, scanFiles)) {
    const lines = tailLines(file, CODEX_TAIL_BYTES);
    for (let i = lines.length - 1; i >= 0; i--) {
      const line = lines[i].trim();
      if (!line || !line.includes('"rate_limits"')) continue;
      let parsed: unknown;
      try {
        parsed = JSON.parse(line);
      } catch {
        continue;
      }
      const limits = findRateLimits(parsed);
      if (!limits) continue;
      const at = toEpochSeconds((parsed as Record<string, unknown>).timestamp);
      return readingFromRateLimits(limits, at, nowMs, staleAfterMinutes);
    }
  }
  return missing("Codex");
}

// ── Both ────────────────────────────────────────────────────────────────

export function collect(settings: Settings, nowMs: number): Reading[] {
  const staleAfter = settings.stale_after_minutes ?? DEFAULT_STALE_AFTER_MINUTES;
  return [
    readClaudeSnapshot(
      expandHome(settings.claude_snapshot_dir ?? DEFAULT_CLAUDE_SNAPSHOT_DIR),
      nowMs,
      staleAfter,
    ),
    readCodexSessions(
      expandHome(settings.codex_sessions_dir ?? DEFAULT_CODEX_SESSIONS_DIR),
      nowMs,
      staleAfter,
      settings.codex_scan_files ?? DEFAULT_CODEX_SCAN_FILES,
    ),
  ];
}
