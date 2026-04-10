import { StreamCoreAIPlugin } from '@streamcore/plugin';
import { google, gmail_v1 } from 'googleapis';
import * as path from 'path';
import * as fs from 'fs';

const envPath = path.join(__dirname, '.env');
if (fs.existsSync(envPath)) {
  const lines = fs.readFileSync(envPath, 'utf-8').split('\n');
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const idx = trimmed.indexOf('=');
    if (idx === -1) continue;
    const key = trimmed.slice(0, idx).trim();
    const value = trimmed.slice(idx + 1).trim().replace(/^["']|["']$/g, '');
    if (!process.env[key]) {
      process.env[key] = value;
    }
  }
}

const plugin = new StreamCoreAIPlugin();

const SCOPES = [
  'https://www.googleapis.com/auth/gmail.readonly',
  'https://www.googleapis.com/auth/gmail.send',
];

const TOKEN_PATH = path.join(__dirname, 'token.json');

function getOAuth2Client() {
  const clientId = process.env.GMAIL_CLIENT_ID;
  const clientSecret = process.env.GMAIL_CLIENT_SECRET;
  const redirectUri = process.env.GMAIL_REDIRECT_URI || 'http://localhost:3000/oauth2callback';

  if (!clientId || !clientSecret) {
    throw new Error(
      'Missing GMAIL_CLIENT_ID or GMAIL_CLIENT_SECRET. ' +
      'Set them in your .env file or environment. See README.md for setup instructions.'
    );
  }

  const oAuth2Client = new google.auth.OAuth2(clientId, clientSecret, redirectUri);

  if (fs.existsSync(TOKEN_PATH)) {
    const token = JSON.parse(fs.readFileSync(TOKEN_PATH, 'utf-8'));
    oAuth2Client.setCredentials(token);
  } else {
    throw new Error(
      'No token.json found. Run the authorization flow first. See README.md for instructions.'
    );
  }

  oAuth2Client.on('tokens', (tokens) => {
    const existing = fs.existsSync(TOKEN_PATH)
      ? JSON.parse(fs.readFileSync(TOKEN_PATH, 'utf-8'))
      : {};
    const merged = { ...existing, ...tokens };
    fs.writeFileSync(TOKEN_PATH, JSON.stringify(merged, null, 2));
    console.error('[gmail] token refreshed and saved');
  });

  return oAuth2Client;
}

function getGmailClient(): gmail_v1.Gmail {
  const auth = getOAuth2Client();
  return google.gmail({ version: 'v1', auth });
}
function decodeBase64Url(data: string): string {
  const base64 = data.replace(/-/g, '+').replace(/_/g, '/');
  return Buffer.from(base64, 'base64').toString('utf-8');
}

function getHeader(headers: gmail_v1.Schema$MessagePartHeader[] | undefined, name: string): string {
  return headers?.find((h) => h.name?.toLowerCase() === name.toLowerCase())?.value ?? '';
}

function extractBody(payload: gmail_v1.Schema$MessagePart | undefined): string {
  if (!payload) return '';

  if (payload.body?.data) {
    return decodeBase64Url(payload.body.data);
  }

  // Multipart — prefer text/plain
  if (payload.parts) {
    const plainPart = payload.parts.find((p) => p.mimeType === 'text/plain');
    if (plainPart?.body?.data) {
      return decodeBase64Url(plainPart.body.data);
    }

    const htmlPart = payload.parts.find((p) => p.mimeType === 'text/html');
    if (htmlPart?.body?.data) {
      const html = decodeBase64Url(htmlPart.body.data);
      return html.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();
    }

    for (const part of payload.parts) {
      const nested = extractBody(part);
      if (nested) return nested;
    }
  }

  return '';
}

async function readEmails(params: Record<string, unknown>): Promise<string> {
  const gmail = getGmailClient();
  const maxResults = Math.min(Number(params.max_results) || 5, 20);
  const query = (params.query as string) || '';
  const defaultQuery = 'category:primary';

  const listRes = await gmail.users.messages.list({
    userId: 'me',
    maxResults,
    labelIds: ['INBOX'],
    q: query || defaultQuery,
  });

  const messages = listRes.data.messages;
  if (!messages || messages.length === 0) {
    return query
      ? `No emails found matching "${query}".`
      : 'Your inbox is empty.';
  }

  const results: string[] = [];

  for (const msg of messages) {
    const detail = await gmail.users.messages.get({
      userId: 'me',
      id: msg.id!,
      format: 'full',
    });

    const headers = detail.data.payload?.headers;
    const from = getHeader(headers, 'From');
    const subject = getHeader(headers, 'Subject');
    const date = getHeader(headers, 'Date');
    const snippet = detail.data.snippet ?? '';
    const body = extractBody(detail.data.payload ?? undefined);

    const preview = body.length > 300 ? body.slice(0, 300) + '…' : body;

    results.push(
      `From: ${from}\nDate: ${date}\nSubject: ${subject}\nPreview: ${preview}`
    );
  }

  return results.join('\n---\n');
}

async function sendEmail(params: Record<string, unknown>): Promise<string> {
  const to = params.to as string;
  const subject = params.subject as string;
  const body = params.body as string;

  if (!to) return 'Missing required parameter: to (recipient email address).';
  if (!subject) return 'Missing required parameter: subject.';
  if (!body) return 'Missing required parameter: body.';

  const gmail = getGmailClient();

  const messageParts = [
    `To: ${to}`,
    `Subject: ${subject}`,
    'Content-Type: text/plain; charset="UTF-8"',
    'MIME-Version: 1.0',
    '',
    body,
  ];
  const rawMessage = messageParts.join('\r\n');
  const encoded = Buffer.from(rawMessage)
    .toString('base64')
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '');

  const res = await gmail.users.messages.send({
    userId: 'me',
    requestBody: { raw: encoded },
  });

  return `Email sent successfully to ${to} (Message ID: ${res.data.id}).`;
}

plugin.onExecute(async (params: Record<string, unknown>) => {
  const action = params.action as string;

  switch (action) {
    case 'read':
      return await readEmails(params);
    case 'send':
      return await sendEmail(params);
    default:
      return `Unknown action "${action}". Use "read" or "send".`;
  }
});

plugin.run();
