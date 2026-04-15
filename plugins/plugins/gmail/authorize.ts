/**
 * One-time authorization helper.
 *
 * Run this script to open a browser, sign in with your Google account, and
 * save the resulting token to token.json so the plugin can access Gmail.
 *
 * Usage:
 *   npx tsx authorize.ts
 */

import { google } from 'googleapis';
import * as fs from 'fs';
import * as path from 'path';
import * as http from 'http';
import * as url from 'url';

// Load .env
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
    if (!process.env[key]) process.env[key] = value;
  }
}

const CLIENT_ID = process.env.GMAIL_CLIENT_ID;
const CLIENT_SECRET = process.env.GMAIL_CLIENT_SECRET;
const REDIRECT_URI = process.env.GMAIL_REDIRECT_URI || 'http://localhost:8090/oauth2callback';
const TOKEN_PATH = path.join(__dirname, 'token.json');

if (!CLIENT_ID || !CLIENT_SECRET) {
  console.error('Error: Set GMAIL_CLIENT_ID and GMAIL_CLIENT_SECRET in .env first.');
  process.exit(1);
}

const SCOPES = [
  'https://www.googleapis.com/auth/gmail.readonly',
  'https://www.googleapis.com/auth/gmail.send',
];

const oAuth2Client = new google.auth.OAuth2(CLIENT_ID, CLIENT_SECRET, REDIRECT_URI);

const authUrl = oAuth2Client.generateAuthUrl({
  access_type: 'offline',
  scope: SCOPES,
  prompt: 'consent',
});

// Parse port from redirect URI
const parsedRedirect = new URL(REDIRECT_URI);
const port = parseInt(parsedRedirect.port, 10) || 8090;
const callbackPath = parsedRedirect.pathname;

const server = http.createServer(async (req, res) => {
  const parsed = url.parse(req.url ?? '', true);
  if (parsed.pathname !== callbackPath) {
    res.writeHead(404);
    res.end('Not found');
    return;
  }

  const code = parsed.query.code as string;
  if (!code) {
    res.writeHead(400);
    res.end('Missing authorization code');
    return;
  }

  try {
    const { tokens } = await oAuth2Client.getToken(code);
    fs.writeFileSync(TOKEN_PATH, JSON.stringify(tokens, null, 2));
    res.writeHead(200, { 'Content-Type': 'text/html' });
    res.end('<h1>Authorization successful!</h1><p>You can close this tab and return to your terminal.</p>');
    console.log(`\nToken saved to ${TOKEN_PATH}`);
    console.log('You can now start the server — the Gmail plugin is ready.');
  } catch (err) {
    res.writeHead(500);
    res.end('Token exchange failed: ' + String(err));
    console.error('Token exchange error:', err);
  }

  // Shut down the temporary server
  setTimeout(() => server.close(() => process.exit(0)), 500);
});

server.listen(port, () => {
  console.log(`\nOpen this URL in your browser to authorize the Gmail plugin:\n`);
  console.log(`  ${authUrl}\n`);
  console.log(`Waiting for callback on ${REDIRECT_URI} ...`);
});
