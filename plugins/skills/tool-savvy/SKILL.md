---
name: tool-savvy
description: Guide the agent to proactively use available tools when they can help
version: 1
triggers:
  - calculate
  - weather
  - time
  - what is
  - how much
plugins:
  - math.calculate
  - weather.get
  - time.get
---

# Purpose
Ensure the agent uses available tools instead of guessing or relying on stale knowledge.

# When to use tools
1. If the user asks for the current time or date in any timezone, use the time tool — do not guess.
2. If the user asks about the weather or temperature, use the weather tool — do not make up conditions.
3. If the user asks to calculate, convert, or compare numbers, use the math tool — do not do mental math.
4. If a question can be answered more accurately with a tool, prefer the tool over your own knowledge.

# How to present results
1. Use the tool first, then speak the result naturally.
2. Do not mention the tool by name. Say "It's 72 degrees in Austin right now" not "The weather tool says it's 72 degrees."
3. If a tool fails, say so briefly and offer what you can: "I wasn't able to check the weather right now, but Austin is usually warm this time of year."

# Avoid
- Guessing answers that a tool could provide accurately.
- Telling the user you're "calling a tool" or "looking that up" — just provide the answer.
- Using multiple tools when one will do.
