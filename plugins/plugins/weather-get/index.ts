// Command weather-get answers "what's the weather" from wttr.in, and puts the
// answer on the caller's screen as a display.card v1 weather card: the
// condition as an icon, today's temperature, and a tile per forecast day.
//
// The spoken sentence stays the full answer. The card is the glanceable half —
// on a device like the NOTE4C it is still on the wall an hour later, which is
// why every line on it is dated.

import { StreamCoreAIPlugin, type Call, type Result } from "@streamcore/plugin";

const CARD_TOPIC = "display.card";
const CARD_VERSION = 1;

/** The panel's own caps. Mirrors note4c-logic/src/display_card.rs. */
const MAX_TITLE = 32;
const MAX_SECONDARY = 48;
const MAX_DETAIL = 80;
const MAX_FORECAST_DAYS = 3;
const MAX_DAY_LABEL = 4;
const MAX_TEMP = 4;
const MAX_NOTE = 34;

/** What the client has an icon for; anything else it draws as a cloud. */
type Icon = "sun" | "moon" | "partly" | "cloud" | "rain" | "storm" | "snow" | "fog" | "wind";

interface Day {
  label: string;
  icon: Icon;
  high: string;
  low: string;
}

interface WeatherPanel {
  icon: Icon;
  unit: string;
  high: string;
  low: string;
  note: string;
  forecast: Day[];
}

interface Card {
  layout: "weather";
  title: string;
  primary: string;
  secondary: string;
  detail: string;
  weather: WeatherPanel;
}

interface Settings {
  /** `"c"` (default) or `"f"`. The spoken sentence still gives both. */
  units?: string;
}

const plugin = new StreamCoreAIPlugin();
let settings: Settings = {};

plugin.onInitialize((init) => {
  settings = (init.config ?? {}) as Settings;
});

/**
 * The server passes a tool's emissions to the client verbatim, so unlike the
 * lifecycle path it does not fill in the turn number. Clients keep the newest
 * card by sequence, so this counts in wall-clock seconds: a weather card asked
 * for now always outranks one asked for earlier, and outranks a card the
 * display projector numbered from the conversation's own small turn counter.
 */
let lastSeq = 0;
function nextSeq(): number {
  const seq = Math.max(Math.floor(Date.now() / 1000), lastSeq + 1);
  lastSeq = seq;
  return seq;
}

plugin.onExecute(async (params: Record<string, unknown>, call: Call): Promise<Result | string> => {
  const location = params.location as string;
  if (!location) {
    return "No location was provided. Please ask the user which city or location they want the weather for.";
  }

  const encoded = encodeURIComponent(location);
  const response = await fetch(`https://wttr.in/${encoded}?format=j1`);
  if (!response.ok) {
    throw new Error(`Weather API returned status ${response.status}`);
  }

  const data = (await response.json()) as WeatherResponse;
  const current = data.current_condition?.[0];
  if (!current) {
    throw new Error(`No weather data found for "${location}"`);
  }

  const fahrenheit = String(settings.units ?? "c").toLowerCase().startsWith("f");
  const seq = nextSeq();

  return {
    speak: buildSpeech(current, data, location),
    emit: [
      {
        topic: CARD_TOPIC,
        payload: {
          type: CARD_TOPIC,
          version: CARD_VERSION,
          session_id: call.sessionId,
          turn_id: `weather_${seq}`,
          turn_seq: seq,
          card: buildCard(data, current, location, fahrenheit),
        },
      },
    ],
  };
});

plugin.run();

/** wttr.in pads a few of its fields, and the client collapses whitespace. */
function clean(text: string | undefined): string {
  return (text ?? "").trim().replace(/\s+/g, " ");
}

function buildSpeech(
  current: CurrentCondition,
  data: WeatherResponse,
  asked: string,
): string {
  const area = data.nearest_area?.[0];
  const areaName = clean(area?.areaName?.[0]?.value) || asked;
  const country = clean(area?.country?.[0]?.value);
  const where = country ? `${areaName}, ${country}` : areaName;
  const description = clean(current.weatherDesc?.[0]?.value) || "Unknown";

  let spoken =
    `Current weather in ${where}: ${description}, ${current.temp_C}°C (${current.temp_F}°F), ` +
    `feels like ${current.FeelsLikeC}°C (${current.FeelsLikeF}°F), humidity ${current.humidity}%, ` +
    `wind ${current.windspeedKmph} km/h.`;

  // The card shows the next days as tiles; the sentence is where the reason to
  // care about them gets said.
  const wet = wettestDay(data);
  if (wet) {
    spoken += ` ${wet.chance}% chance of rain on ${wet.name}.`;
  }
  return spoken;
}

function buildCard(
  data: WeatherResponse,
  current: CurrentCondition,
  asked: string,
  fahrenheit: boolean,
): Card {
  const temp = (c: string | undefined, f: string | undefined): string =>
    String((fahrenheit ? f : c) ?? "").slice(0, MAX_TEMP);
  const today = data.weather?.[0];

  return {
    layout: "weather",
    // The place the person asked about, not wttr.in's nearest weather station:
    // it answers "Auckland" with "Newton", which on a panel reads as the wrong
    // city. The spoken line still names the station and its country.
    title: clean(asked).slice(0, MAX_TITLE),
    primary: temp(current.temp_C, current.temp_F),
    secondary: clean(current.weatherDesc?.[0]?.value).slice(0, MAX_SECONDARY),
    detail: today ? dayAndDate(today.date).slice(0, MAX_DETAIL) : "",
    weather: {
      icon: iconFor(current.weatherCode),
      unit: fahrenheit ? "F" : "C",
      high: temp(today?.maxtempC, today?.maxtempF),
      low: temp(today?.mintempC, today?.mintempF),
      note: advice(data).slice(0, MAX_NOTE),
      // Today is already the headline, so the tiles start at tomorrow.
      forecast: (data.weather ?? []).slice(1, 1 + MAX_FORECAST_DAYS).map((day) => ({
        label: shortWeekday(day.date).slice(0, MAX_DAY_LABEL),
        icon: iconFor(middayCode(day)),
        high: temp(day.maxtempC, day.maxtempF),
        low: temp(day.mintempC, day.mintempF),
      })),
    },
  };
}

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
/** A tile is four characters wide, and "SATU" is not a day. */
const SHORT_DAYS = ["SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"];
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** wttr.in dates are `YYYY-MM-DD` at the queried location, so they are read as
 * plain calendar days rather than instants — parsing one as UTC and printing it
 * locally can land on the day before. */
function parseDay(date: string | undefined): Date | null {
  const parts = (date ?? "").split("-").map(Number);
  if (parts.length !== 3 || parts.some((n) => !Number.isFinite(n))) return null;
  return new Date(parts[0], parts[1] - 1, parts[2]);
}

function weekday(date: string | undefined): string {
  const at = parseDay(date);
  return at ? DAYS[at.getDay()] : "";
}

function shortWeekday(date: string | undefined): string {
  const at = parseDay(date);
  return at ? SHORT_DAYS[at.getDay()] : "";
}

/** "Saturday 18 May" — the card's own line for what day it is showing. */
function dayAndDate(date: string | undefined): string {
  const at = parseDay(date);
  if (!at) return "";
  return `${DAYS[at.getDay()]} ${at.getDate()} ${MONTHS[at.getMonth()]}`;
}

/**
 * The condition a day is remembered by is the one it has in the middle of it,
 * not the one it opens with at 3am.
 */
function middayCode(day: ForecastDay): string | undefined {
  const hourly = day.hourly ?? [];
  const noon = hourly.find((hour) => hour.time === "1200" || hour.time === "1300");
  return (noon ?? hourly[Math.floor(hourly.length / 2)])?.weatherCode;
}

/**
 * WWO condition codes, grouped down to the icons the client draws. The list is
 * the published set; a code outside it falls through to a cloud, which is what
 * the client does with an icon name it does not know.
 */
function iconFor(code: string | undefined): Icon {
  switch (Number(code)) {
    case 113:
      return "sun";
    case 116:
      return "partly";
    case 119:
    case 122:
      return "cloud";
    case 143:
    case 248:
    case 260:
      return "fog";
    case 200:
    case 386:
    case 389:
    case 392:
    case 395:
      return "storm";
    case 179:
    case 182:
    case 185:
    case 227:
    case 230:
    case 281:
    case 284:
    case 311:
    case 314:
    case 317:
    case 320:
    case 323:
    case 326:
    case 329:
    case 332:
    case 335:
    case 338:
    case 350:
    case 362:
    case 365:
    case 368:
    case 371:
    case 374:
    case 377:
      return "snow";
    case 176:
    case 263:
    case 266:
    case 293:
    case 296:
    case 299:
    case 302:
    case 305:
    case 308:
    case 353:
    case 356:
    case 359:
      return "rain";
    default:
      return "cloud";
  }
}

/** The wettest of the coming days, once it is wet enough to be worth a word. */
const RAIN_WORTH_MENTIONING = 50;

function wettestDay(data: WeatherResponse): { name: string; chance: number } | null {
  let best: { name: string; chance: number } | null = null;
  for (const day of (data.weather ?? []).slice(1, 1 + MAX_FORECAST_DAYS)) {
    const chance = Math.max(
      0,
      ...(day.hourly ?? []).map((hour) => Number(hour.chanceofrain ?? 0) || 0),
    );
    if (chance >= RAIN_WORTH_MENTIONING && (!best || chance > best.chance)) {
      best = { name: weekday(day.date), chance };
    }
  }
  return best;
}

/** The one line of advice across the foot of the card, or nothing. */
function advice(data: WeatherResponse): string {
  const wet = wettestDay(data);
  return wet && wet.name ? `Take an umbrella ${wet.name}` : "";
}

// Type definitions for the wttr.in JSON response
interface CurrentCondition {
  temp_C: string;
  temp_F: string;
  humidity: string;
  windspeedKmph: string;
  FeelsLikeC: string;
  FeelsLikeF: string;
  weatherCode?: string;
  weatherDesc?: Array<{ value: string }>;
}

interface ForecastDay {
  date?: string;
  maxtempC?: string;
  maxtempF?: string;
  mintempC?: string;
  mintempF?: string;
  hourly?: Array<{ time?: string; weatherCode?: string; chanceofrain?: string }>;
}

interface WeatherResponse {
  current_condition?: CurrentCondition[];
  weather?: ForecastDay[];
  nearest_area?: Array<{
    areaName?: Array<{ value: string }>;
    country?: Array<{ value: string }>;
  }>;
}
