// Where the numbers come from. Neither agent exposes a query API for its own
// rate limits, so both readings are scavenged from files the tools already
// write while they run.

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";

/**
 * What each agent is called on the card and in the spoken line. Claude Code
 * rather than Claude: the reading comes from the CLI's own files, and on a
 * panel sitting beside a Codex panel the product name is the useful one.
 */
export const CLAUDE = "Claude Code";
export const CODEX = "Codex";

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
  /**
   * Windows the plan meters apart from the two above — one model with its own
   * weekly limit, say. Absent for a source that has no such concept.
   */
  scoped?: ScopedWindow[];
}

export interface ScopedWindow {
  label: string;
  percent: number | null;
  resetsAt: number | null;
}

export interface Settings {
  /** claude-hud's external usage snapshot — the freshest Claude Code source. */
  claude_usage_file?: string;
  /** Legacy statusline snapshot directory, used when the file above is absent. */
  claude_snapshot_dir?: string;
  /** Ask the Codex CLI for live limits before falling back to its logs. */
  codex_live?: boolean;
  codex_command?: string;
  codex_timeout_ms?: number;
  codex_sessions_dir?: string;
  stale_after_minutes?: number;
  codex_scan_files?: number;
}

export const DEFAULT_CLAUDE_USAGE_FILE = "~/.claude/usage-snapshot.json";
export const DEFAULT_CLAUDE_SNAPSHOT_DIR =
  "~/.claude/plugins/claude-hud/eink-snapshots";
export const DEFAULT_CODEX_COMMAND = "codex";
export const DEFAULT_CODEX_TIMEOUT_MS = 6_000;
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
  const reading = missing(CLAUDE);

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
    agent: CLAUDE,
    fiveHourPercent: clampPercent(best.usage.fiveHour),
    weeklyPercent: clampPercent(best.usage.sevenDay),
    fiveHourResetsAt: toEpochSeconds(best.usage.fiveHourResetAt),
    weeklyResetsAt: toEpochSeconds(best.usage.sevenDayResetAt),
    ageMinutes: age,
    sampledAt: best.at,
    stale: age !== null && age > staleAfterMinutes,
  };
}

/**
 * The snapshot claude-hud writes for other tools, which is the freshest Claude
 * Code source there is. Claude Code hands its plan percentages to the
 * statusline on stdin and nowhere else — there is no file of its own and no
 * local API to ask — so something that already sees that stdin has to tee it.
 * claude-hud does, on `display.externalUsageWritePath`, atomically and at most
 * every 30 seconds:
 *
 *   { "updated_at": "2026-09-11T09:58:00.000Z",
 *     "five_hour": { "used_percentage": 62, "resets_at": "..." },
 *     "seven_day": { "used_percentage": 17, "resets_at": "..." } }
 *
 * It is therefore only as fresh as the last time Claude Code drew its
 * statusline. That is the ceiling on this number, and why the age travels with
 * it rather than being rounded off.
 */
export function readClaudeUsageFile(
  file: string,
  nowMs: number,
  staleAfterMinutes: number,
): Reading {
  let parsed: Record<string, unknown>;
  try {
    parsed = JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return missing(CLAUDE);
  }

  const window = (key: string): Record<string, unknown> | null => {
    const value = parsed[key];
    return value && typeof value === "object" ? (value as Record<string, unknown>) : null;
  };
  const fiveHour = window("five_hour");
  const sevenDay = window("seven_day");
  if (!fiveHour && !sevenDay) return missing(CLAUDE);

  // A file with no timestamp still has an mtime, and the writer replaces it by
  // rename, so the mtime is when these numbers were true.
  let at = toEpochSeconds(parsed.updated_at);
  if (at === null) {
    try {
      at = Math.round(fs.statSync(file).mtimeMs / 1000);
    } catch {
      return missing(CLAUDE);
    }
  }

  const age = ageMinutes(at, nowMs);
  return {
    agent: CLAUDE,
    scoped: parseScopedWindows(parsed.model_scoped),
    fiveHourPercent: clampPercent(fiveHour?.used_percentage),
    weeklyPercent: clampPercent(sevenDay?.used_percentage),
    fiveHourResetsAt: toEpochSeconds(fiveHour?.resets_at),
    weeklyResetsAt: toEpochSeconds(sevenDay?.resets_at),
    ageMinutes: age,
    sampledAt: at ?? 0,
    stale: age !== null && age > staleAfterMinutes,
  };
}

/**
 * `model_scoped` from the statusline payload: a plan can meter one model apart
 * from the rest, and that window is invisible in the five-hour and weekly
 * numbers. The panel shows it as another gauge rather than folding it in.
 */
export function parseScopedWindows(value: unknown): ScopedWindow[] {
  if (!Array.isArray(value)) return [];
  const windows: ScopedWindow[] = [];
  for (const raw of value.slice(0, 3)) {
    if (!raw || typeof raw !== "object") continue;
    const entry = raw as Record<string, unknown>;
    const label = typeof entry.display_name === "string" ? entry.display_name.trim() : "";
    const percent = clampPercent(entry.used_percentage ?? entry.utilization);
    if (!label || percent === null) continue;
    windows.push({ label, percent, resetsAt: toEpochSeconds(entry.resets_at) });
  }
  return windows;
}

/** True once a reading carries at least one number worth showing. */
export function hasNumbers(reading: Reading): boolean {
  return reading.fiveHourPercent !== null || reading.weeklyPercent !== null;
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
 * The Codex CLI's own app-server answers `account/rateLimits/read` with the
 * live figures, which is as current as Codex itself — it goes and asks, rather
 * than reporting what some past session happened to see.
 *
 * Three lines of JSON-RPC over stdio and about a second. Codex uses its own
 * stored credentials; nothing here reads or forwards a token.
 *
 *   -> {"id":1,"method":"initialize","params":{"clientInfo":{...}}}
 *   -> {"method":"initialized","params":{}}
 *   -> {"id":2,"method":"account/rateLimits/read","params":{}}
 *   <- {"id":2,"result":{"rateLimits":{"primary":{"usedPercent":0,
 *        "windowDurationMins":300,"resetsAt":1789141035}, "secondary":{...}}}}
 */
export function readCodexLive(settings: Settings, nowMs: number): Promise<Reading | null> {
  const command = settings.codex_command ?? DEFAULT_CODEX_COMMAND;
  const timeoutMs = settings.codex_timeout_ms ?? DEFAULT_CODEX_TIMEOUT_MS;

  return new Promise((resolve) => {
    let child: ReturnType<typeof spawn>;
    try {
      // stderr is ignored rather than inherited: the app-server is chatty on a
      // first run and none of it belongs in the voice server's log.
      child = spawn(command, ["app-server"], { stdio: ["pipe", "pipe", "ignore"] });
    } catch {
      resolve(null);
      return;
    }

    let settled = false;
    const finish = (reading: Reading | null) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      // The app-server keeps running until its stdin closes; a session that
      // never got its answer must not leave one behind.
      child.kill("SIGKILL");
      resolve(reading);
    };
    const timer = setTimeout(() => finish(null), timeoutMs);

    child.on("error", () => finish(null));
    // `close` rather than `exit`: exit can arrive before the last of stdout has
    // been read, which would throw away an answer that did come.
    child.on("close", () => finish(null));

    let buffered = "";
    child.stdout?.on("data", (chunk: Buffer) => {
      buffered += chunk.toString("utf8");
      let newline: number;
      while ((newline = buffered.indexOf("\n")) >= 0) {
        const line = buffered.slice(0, newline);
        buffered = buffered.slice(newline + 1);
        if (!line.trim()) continue;
        let message: { id?: unknown; result?: unknown };
        try {
          message = JSON.parse(line);
        } catch {
          continue;
        }
        if (message.id !== RATE_LIMIT_REQUEST_ID) continue;
        const limits = normalizeLiveRateLimits(message.result);
        finish(limits ? readingFromRateLimits(limits, Math.round(nowMs / 1000), nowMs, 0) : null);
      }
    });

    const send = (value: unknown) => child.stdin?.write(`${JSON.stringify(value)}\n`);
    send({
      id: 1,
      method: "initialize",
      params: { clientInfo: { name: "streamcore-ai-usage", title: "StreamCore", version: "1" } },
    });
    send({ method: "initialized", params: {} });
    send({ id: RATE_LIMIT_REQUEST_ID, method: "account/rateLimits/read", params: {} });
  });
}

const RATE_LIMIT_REQUEST_ID = 2;

/**
 * The app-server names the same two windows in camelCase and gives the window
 * length in `windowDurationMins`. Normalising to the rollout logs' shape keeps
 * one place deciding which window is the rolling one.
 */
export function normalizeLiveRateLimits(result: unknown): CodexRateLimits | null {
  if (!result || typeof result !== "object") return null;
  const rateLimits = (result as Record<string, unknown>).rateLimits;
  if (!rateLimits || typeof rateLimits !== "object") return null;

  const convert = (value: unknown) => {
    if (!value || typeof value !== "object") return undefined;
    const window = value as Record<string, unknown>;
    return {
      used_percent: window.usedPercent,
      window_minutes: window.windowDurationMins,
      resets_at: window.resetsAt,
    };
  };

  const snapshot = rateLimits as Record<string, unknown>;
  const primary = convert(snapshot.primary);
  const secondary = convert(snapshot.secondary);
  if (!primary && !secondary) return null;
  return { primary, secondary };
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
    agent: CODEX,
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
  return missing(CODEX);
}

// ── Both ────────────────────────────────────────────────────────────────

/**
 * Both agents, each from the freshest source that answers.
 *
 * Neither tool publishes its limits to a file of its own, so each has a live
 * route and a scavenged one, and the live route is tried first:
 *
 * | Agent | Live | Fallback |
 * | --- | --- | --- |
 * | Claude Code | claude-hud's usage snapshot, refreshed while the CLI is used | the older statusline snapshots |
 * | Codex | `codex app-server`, which goes and asks | `rate_limits` scavenged from its rollout logs |
 *
 * A fallback reading is not wrong, only old, so it is kept with its age
 * attached rather than discarded — an hour-old number beats no number on a
 * panel. What must not happen is an old number presented as current, which is
 * what `stale` exists for.
 */
export async function collect(settings: Settings, nowMs: number): Promise<Reading[]> {
  const staleAfter = settings.stale_after_minutes ?? DEFAULT_STALE_AFTER_MINUTES;
  return [
    collectClaude(settings, nowMs, staleAfter),
    await collectCodex(settings, nowMs, staleAfter),
  ];
}

export function collectClaude(settings: Settings, nowMs: number, staleAfter: number): Reading {
  const fresh = readClaudeUsageFile(
    expandHome(settings.claude_usage_file ?? DEFAULT_CLAUDE_USAGE_FILE),
    nowMs,
    staleAfter,
  );
  if (hasNumbers(fresh)) return fresh;
  return readClaudeSnapshot(
    expandHome(settings.claude_snapshot_dir ?? DEFAULT_CLAUDE_SNAPSHOT_DIR),
    nowMs,
    staleAfter,
  );
}

export async function collectCodex(
  settings: Settings,
  nowMs: number,
  staleAfter: number,
): Promise<Reading> {
  if (settings.codex_live ?? true) {
    const live = await readCodexLive(settings, nowMs);
    if (live && hasNumbers(live)) return live;
  }
  return readCodexSessions(
    expandHome(settings.codex_sessions_dir ?? DEFAULT_CODEX_SESSIONS_DIR),
    nowMs,
    staleAfter,
    settings.codex_scan_files ?? DEFAULT_CODEX_SCAN_FILES,
  );
}
