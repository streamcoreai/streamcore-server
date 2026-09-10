// Turning readings into the two things a voice turn produces: a sentence to
// say, and one `display.card` v1 packet for whatever screen is on the call.

import type { Reading } from "./sources";

export const CARD_TOPIC = "display.card";
export const CARD_VERSION = 1;

const BAR_WIDTH = 10;
const MAX_TITLE = 32;

/** The panel's own caps for a split card. Mirrors note4c-logic/src/display_card.rs. */
const MAX_COLUMN_LINES = 7;
const MAX_COLUMN_CHARS = 17;

export interface Column {
  heading: string;
  lines: string[];
}

/** The v1 card. Semantics only: no coordinates, fonts, or colours. */
export interface Card {
  layout: "split";
  title: string;
  columns: Column[];
}

/**
 * Bars are drawn from `=`, `-` and `.` rather than block glyphs for two
 * reasons: the note4c maps every non-ASCII character to `?` before rendering,
 * and `#`, `*` and brackets are rejected as markup by the card sanitizer the
 * projector applies. This alphabet survives both.
 */
export function bar(percent: number | null): string {
  if (percent === null) return ".".repeat(BAR_WIDTH);
  const filled = Math.max(0, Math.min(BAR_WIDTH, Math.round((percent / 100) * BAR_WIDTH)));
  return "=".repeat(filled) + "-".repeat(BAR_WIDTH - filled);
}

/** "2h 10m", or "" when there is nothing to say. */
export function untilReset(resetsAt: number | null, nowMs: number): string {
  if (resetsAt === null) return "";
  const minutes = Math.round((resetsAt - nowMs / 1000) / 60);
  if (minutes <= 0) return "";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours >= 24) return `${Math.floor(hours / 24)}d`;
  return `${hours}h ${minutes % 60}m`;
}

/** How old a reading is, in the words a person would use. */
function age(minutes: number): string {
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function pad(value: number): string {
  return value < 10 ? `0${value}` : `${value}`;
}

/** "14:30" in the server's local time, which is the clock the user reads. */
export function clock(epochSeconds: number): string {
  const at = new Date(epochSeconds * 1000);
  return `${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

/** "Sep 8" — for a reset far enough out that a bare clock time is ambiguous. */
export function calendarDay(epochSeconds: number): string {
  const at = new Date(epochSeconds * 1000);
  return `${MONTHS[at.getMonth()]} ${at.getDate()}`;
}

/**
 * When a reading was taken. A clock time while it is same-day, a date once it
 * is not: "as of 06:11" on a two-day-old sample would read as this morning.
 */
export function takenAt(epochSeconds: number, nowMs: number): string {
  const sameDay = new Date(epochSeconds * 1000).toDateString() === new Date(nowMs).toDateString();
  return sameDay ? clock(epochSeconds) : calendarDay(epochSeconds);
}

/**
 * When a window rolls over. The five-hour window always lands within the day
 * or the next morning, so a clock time is unambiguous; the weekly one can be
 * six days out and needs the date.
 */
function resetStamp(epochSeconds: number | null, short: boolean): string {
  if (epochSeconds === null) return "";
  return short ? `@${clock(epochSeconds)}` : `@${calendarDay(epochSeconds)}`;
}

/**
 * One window, as the three rows the panel gives it: what has gone, a bar, and
 * what is left with the countdown to the reset.
 */
function windowRows(label: string, percent: number | null, resetsAt: number | null): string[] {
  if (percent === null) {
    return [`${label} no data`, bar(null), ""];
  }
  const stamp = resetStamp(resetsAt, label === "5H");
  const left = `${100 - percent}% left`;
  return [`${label} ${percent}% used`, bar(percent), stamp ? `${left} ${stamp}` : left];
}

/**
 * A column per agent, opening with when its numbers were taken. Absolute times
 * beat relative ones on a panel that may sit unrefreshed for an hour: "as of
 * 06:11" stays true on the screen, "2m ago" does not.
 */
export function buildColumn(reading: Reading, nowMs: number): Column {
  if (reading.ageMinutes === null) {
    return { heading: reading.agent, lines: ["no recent runs", "on this machine"] };
  }
  // Seven rows is the whole budget, so the reset time rides on the same row as
  // the percentage left rather than taking one of its own.
  const lines = [
    `as of ${takenAt(reading.sampledAt, nowMs)}`,
    ...windowRows("5H", reading.fiveHourPercent, reading.fiveHourResetsAt),
    ...windowRows("7D", reading.weeklyPercent, reading.weeklyResetsAt),
  ];
  return {
    heading: reading.agent,
    lines: lines.slice(0, MAX_COLUMN_LINES).map((line) => line.slice(0, MAX_COLUMN_CHARS)),
  };
}

export function buildCard(readings: Reading[], nowMs: number): Card {
  return {
    layout: "split",
    title: "AI Usage".slice(0, MAX_TITLE),
    columns: readings.slice(0, 2).map((reading) => buildColumn(reading, nowMs)),
  };
}

function percentPhrase(percent: number | null): string {
  return percent === null ? "unknown" : `${percent} percent`;
}

/**
 * The panel is glanceable and abbreviated; the sentence is where the numbers
 * get said in full, including the ones the columns had no room for. Anything
 * the files did not answer is said out loud as unknown rather than rounded to
 * zero.
 */
export function buildSpeech(readings: Reading[], nowMs: number): string {
  const sentences: string[] = [];

  for (const reading of readings) {
    if (reading.ageMinutes === null) {
      sentences.push(`I couldn't find any recent ${reading.agent} usage on this machine.`);
      continue;
    }

    const parts: string[] = [];
    for (const [label, percent, resetsAt, sameDay] of [
      ["five-hour limit", reading.fiveHourPercent, reading.fiveHourResetsAt, true],
      ["week", reading.weeklyPercent, reading.weeklyResetsAt, false],
    ] as const) {
      if (percent === null) {
        parts.push(`its ${label} is ${percentPhrase(percent)}`);
        continue;
      }
      const remaining = untilReset(resetsAt, nowMs);
      // A clock time for a reset a week out says nothing useful; the weekly
      // window gets the date instead.
      const when = resetsAt === null ? "" : sameDay ? `at ${clock(resetsAt)},` : `on ${calendarDay(resetsAt)},`;
      const spoken = remaining ? `, resetting ${when} in ${spokenDuration(remaining)}` : "";
      parts.push(
        `${percentPhrase(percent)} of its ${label}, ${100 - percent} percent left${spoken}`,
      );
    }

    let sentence = `${reading.agent} has used ${parts.join(", and ")}`;
    sentence += `. Read at ${clock(reading.sampledAt)}`;
    if (reading.stale) {
      sentence += `, ${spokenDuration(age(reading.ageMinutes))} ago`;
    }
    sentences.push(sentence + ".");
  }

  return sentences.join(" ");
}

/** "2h 10m" reads badly out loud; "2 hours 10 minutes" does not. */
function spokenDuration(compact: string): string {
  return compact
    .replace(/(\d+)d/, (_, n) => `${n} ${n === "1" ? "day" : "days"}`)
    .replace(/(\d+)h/, (_, n) => `${n} ${n === "1" ? "hour" : "hours"}`)
    .replace(/(\d+)m/, (_, n) => `${n} ${n === "1" ? "minute" : "minutes"}`);
}
