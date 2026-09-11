// Turning readings into the two things a voice turn produces: a sentence to
// say, and one `display.card` v1 packet for whatever screen is on the call.

import type { Reading } from "./sources";

export const CARD_TOPIC = "display.card";
export const CARD_VERSION = 1;

const MAX_TITLE = 32;

/** The panel's own caps for a usage card. Mirrors note4c-logic/src/display_card.rs. */
const MAX_AGENTS = 3;
const MAX_AGENT_NAME = 18;
const MAX_AGENT_NOTE = 14;
const MAX_GAUGES = 3;
const MAX_GAUGE_LABEL = 5;
const MAX_GAUGE_RESET = 8;

/** One rate-limit window. */
export interface Gauge {
  /** `"5H"`, `"7D"`. */
  label: string;
  /**
   * Used, as a whole percentage. `null` where the source did not say — the
   * panel draws that apart from zero rather than claiming an untouched limit.
   */
  percent: number | null;
  /** When the window rolls over, already formatted: `"@14:10"`. */
  reset: string;
}

export interface Agent {
  name: string;
  /** When the reading was taken, or why there is none. */
  note: string;
  gauges: Gauge[];
}

/**
 * The v1 card. Semantics only: no coordinates, fonts, or colours — the client
 * decides what a percentage looks like, and the note4c draws it as a gauge.
 */
export interface Card {
  layout: "usage";
  title: string;
  agents: Agent[];
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

function gauge(label: string, percent: number | null, resetsAt: number | null): Gauge {
  return {
    label: label.toUpperCase().slice(0, MAX_GAUGE_LABEL),
    percent,
    // Only the rolling window lands inside the day; everything else needs the
    // date, since "@16:00" says nothing about a reset six days out.
    reset: resetStamp(resetsAt, label === "5H").slice(0, MAX_GAUGE_RESET),
  };
}

/**
 * A panel per agent, labelled with when its numbers were taken. Absolute times
 * beat relative ones on a panel that may sit unrefreshed for an hour: "as of
 * 06:11" stays true on the screen, "2m ago" does not.
 *
 * An agent that has not run recently still gets its panel, with the note
 * carrying the reason: a card that quietly dropped it would look like an agent
 * with nothing used.
 */
export function buildAgent(reading: Reading, nowMs: number): Agent {
  const name = reading.agent.slice(0, MAX_AGENT_NAME);
  if (reading.ageMinutes === null) {
    return { name, note: "no recent runs", gauges: [] };
  }
  // A reading old enough to have moved says so in the one line it has. The
  // panel may sit unrefreshed for hours on top of that, so "as of 06:11" on a
  // number taken last week reads as this morning unless the word is there.
  const when = takenAt(reading.sampledAt, nowMs);
  return {
    name,
    note: (reading.stale ? `stale ${when}` : `as of ${when}`).slice(0, MAX_AGENT_NOTE),
    gauges: [
      gauge("5H", reading.fiveHourPercent, reading.fiveHourResetsAt),
      gauge("7D", reading.weeklyPercent, reading.weeklyResetsAt),
      // A separately metered model goes after the two everyone has, and is
      // labelled with its own name rather than a window length.
      ...(reading.scoped ?? []).map((w) => gauge(w.label, w.percent, w.resetsAt)),
    ].slice(0, MAX_GAUGES),
  };
}

export function buildCard(readings: Reading[], nowMs: number): Card {
  return {
    layout: "usage",
    title: "AI Usage".slice(0, MAX_TITLE),
    agents: readings.slice(0, MAX_AGENTS).map((reading) => buildAgent(reading, nowMs)),
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
    // A separately metered model is its own budget; folding it into the week
    // above would double-count it and hide the one that runs out first.
    for (const window of reading.scoped ?? []) {
      if (window.percent === null) continue;
      sentence += `. Its ${window.label} week is ${percentPhrase(window.percent)} used, ${100 - window.percent} percent left`;
    }
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
