---
name: gmail-assistant
description: Guide the agent through reading emails and offering to reply via voice
version: 1
triggers:
  - email
  - gmail
  - inbox
  - mail
  - unread
  - send email
  - reply
  - check email
  - check my email
  - read my email
plugins:
  - gmail
---

# Purpose
Handle Gmail interactions naturally in a voice conversation, walking the user through emails one at a time and offering to reply.

# Reading emails
1. When the user asks to check their email, **immediately call the tool** — do NOT say anything like "Let me check" first. A thinking sound plays automatically while the tool runs, so the user already gets feedback.
2. Use the `gmail` tool with action "read" to fetch recent messages.
2. Do NOT read all emails at once. Present them **one at a time**, starting with the most recent.
3. For each email, summarize it briefly: who it's from, the subject, and a one-sentence summary of the content. Do not read the entire body unless asked.
4. After summarizing each email, ask: "Would you like to reply to this one, skip to the next, or stop?"
5. If the user says "skip" or "next," move to the next email and repeat.
6. If the user says "stop" or "that's it," end the email review.

# Replying to emails
1. When the user wants to reply, ask: "What would you like to say?"
2. Listen to their spoken response — this becomes the reply body.
3. **Separate commands from content.** The user is speaking to you in natural language, so their response will often mix instructions to you with the actual email message. You must figure out which part is directed at you (e.g. "let's reply", "the subject should be", "yeah send it", "go ahead") and which part is the actual email body. Only include the email content in the message — strip out anything that is clearly a command or conversation with you.
4. Clean up speech artifacts. Since the user is dictating, remove filler words like "uh", "um", "like", and false starts. Make the email read naturally as written text while preserving the user's intended meaning and tone.
5. **MANDATORY CONFIRMATION — never skip this step.** After cleaning up the message, read the full email body back to the user word-for-word and ask explicitly: "Here's what I'll send: [full cleaned message]. Does that sound good, or would you like to change anything?"
6. Wait for a clear "yes", "send it", "sounds good", or similar affirmative. Vague responses like "sure, whatever" count as confirmation. BUT if the user adds new content, corrections, or says "actually…" or "change…" — treat that as a revision, update the draft, and go back to step 5.
7. **Do NOT call the gmail tool with action "send" until the user has explicitly confirmed.** This is a hard rule — no exceptions.
8. Once confirmed, **immediately call the gmail tool with action "send"** — do NOT say anything first. A thinking sound plays automatically while the tool runs. After the tool succeeds, confirm briefly: "Sent."
9. If the user says "no" or wants to change something, ask what they'd like to change, apply the edit, and repeat from step 5.
10. After sending, confirm briefly: "Sent." Then offer to continue with the next email.

# Composing new emails
1. If the user wants to send a new email (not a reply), ask for the recipient, subject, and body one at a time.
2. Read back the full message and ask for confirmation before sending.
3. If the user gives all the details at once, that's fine — still confirm before sending.

# Voice-friendly guidelines
1. Keep summaries short — the user is listening, not reading.
2. Don't read email headers like "From: John Smith <john@example.com>." Just say "from John Smith."
3. Don't mention message IDs, timestamps in ISO format, or technical details.
4. Dates should be natural: "this morning," "yesterday," "last Tuesday."
5. If an email body is long, summarize the key point in one sentence.

# Avoid
- Reading all emails in a single long monologue.
- Sending an email without explicit user confirmation.
- Asking the user to spell out email addresses if they've already been mentioned in the conversation (e.g., replying to someone who just emailed them).
- Repeating "Would you like me to help with anything else?" after every action.
