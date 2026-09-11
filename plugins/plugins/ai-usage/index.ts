// Command ai-usage answers "how much AI have I used" from files Claude Code and
// Codex leave on the machine, and puts the answer on the caller's screen as a
// display.card v1 usage card: one panel per agent, a gauge per window.
//
// It reads only; it never signs in to either service and never leaves the host.

import { StreamCoreAIPlugin, type Call, type Result } from "@streamcore/plugin";
import { buildCard, buildSpeech, CARD_TOPIC, CARD_VERSION } from "./card";
import { collect, type Settings } from "./sources";

const plugin = new StreamCoreAIPlugin();
let settings: Settings = {};

plugin.onInitialize((init) => {
  settings = (init.config ?? {}) as Settings;
});

/**
 * The server passes a tool's emissions to the client verbatim, so unlike the
 * lifecycle path it does not fill in the turn number. Clients keep the newest
 * card by sequence, so this counts in wall-clock seconds: a usage card asked
 * for now always outranks one asked for earlier, and outranks a card the
 * display projector numbered from the conversation's own small turn counter.
 *
 * The cost is that with the projector enabled, its cards stop winning for the
 * rest of the session once a usage card has been shown.
 */
let lastSeq = 0;
function nextSeq(): number {
  const seq = Math.max(Math.floor(Date.now() / 1000), lastSeq + 1);
  lastSeq = seq;
  return seq;
}

plugin.onExecute(async (_params: Record<string, unknown>, call: Call): Promise<Result> => {
  const now = Date.now();
  // Asking Codex costs about a second, which is why the manifest's timeout
  // has room for it.
  const readings = await collect(settings, now);
  const seq = nextSeq();

  return {
    speak: buildSpeech(readings, now),
    emit: [
      {
        topic: CARD_TOPIC,
        payload: {
          type: CARD_TOPIC,
          version: CARD_VERSION,
          session_id: call.sessionId,
          turn_id: `usage_${seq}`,
          turn_seq: seq,
          card: buildCard(readings, now),
        },
      },
    ],
  };
});

plugin.run();
