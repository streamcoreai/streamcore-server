# Gmail Plugin

Read and send emails through Gmail using the Google Gmail API with OAuth2.

## Features

| Action | Description |
| ------ | ----------- |
| `read` | Fetch recent emails (optionally filtered by a Gmail search query) |
| `send` | Send a plain-text email to a recipient |

## Prerequisites

- **Node.js 18+** and **npm**
- A Google Cloud project with the Gmail API enabled
- OAuth2 credentials (Client ID + Client Secret)

---

## Google Cloud Setup

### 1. Create a Google Cloud Project

1. Go to the [Google Cloud Console](https://console.cloud.google.com/).
2. Click the project dropdown at the top, then click **New Project**.
3. Give it a name (e.g. `voiceagent-gmail`) and click **Create**.
4. Select the new project from the dropdown.

### 2. Enable the Gmail API

1. Navigate to **APIs & Services → Library**.
2. Search for **Gmail API**.
3. Click on it and press **Enable**.

### 3. Configure the OAuth Consent Screen

1. Go to **APIs & Services → OAuth consent screen**.
2. Select **External** (or **Internal** if you're on Google Workspace and only need org access).
3. Fill in the required fields:
   - **App name** — e.g. `VoiceAgent Gmail Plugin`
   - **User support email** — your email
   - **Developer contact** — your email
4. Click **Save and Continue**.
5. On the **Scopes** page, click **Add or Remove Scopes** and add:
   - `https://www.googleapis.com/auth/gmail.readonly`
   - `https://www.googleapis.com/auth/gmail.send`
6. Click **Save and Continue** through the remaining steps.

> **Note:** While the app is in "Testing" status, only test users you add can authorize. Add your own Google account under **Test users**.

### 4. Create OAuth2 Credentials

1. Go to **APIs & Services → Credentials**.
2. Click **Create Credentials → OAuth client ID**.
3. Choose **Web application**.
4. Set the **Authorized redirect URIs** to:
   ```
   http://localhost:3000/oauth2callback
   ```
5. Click **Create**.
6. Copy the **Client ID** and **Client Secret**.

---

## Plugin Setup

### 1. Install Dependencies

```bash
cd plugins/plugins/gmail
npm install
```

### 2. Configure Credentials

Copy the example env file and fill in your credentials:

```bash
cp .env.example .env
```

Edit `.env`:

```env
GMAIL_CLIENT_ID=your-client-id-here.apps.googleusercontent.com
GMAIL_CLIENT_SECRET=your-client-secret-here
GMAIL_REDIRECT_URI=http://localhost:3000/oauth2callback
```

### 3. Authorize (One-Time)

Run the authorization helper to sign in and save a token:

```bash
npx tsx authorize.ts
```

This will:
1. Print a URL — open it in your browser.
2. Sign in with your Google account and grant access.
3. Redirect back to `localhost:3000` and save `token.json`.

You only need to do this once. The token refreshes automatically.

### 4. Start the Server

Start (or restart) the voice agent server. The Gmail plugin will be loaded automatically.

---

## Usage Examples

Once the plugin is running, you can talk to the voice agent naturally:

- *"Do I have any new emails?"*
- *"Check my email for messages from Alice"*
- *"Send an email to bob@example.com saying I'll be 10 minutes late"*
- *"Read my unread emails"*

The plugin requires confirmation before executing (set via `confirmation_required: true`), so the agent will ask you to confirm before reading or sending.

The plugin also has `thinking_sound: true`, which plays a soft looping tone through the audio stream while the Gmail API call is in progress, so the user knows something is happening during the wait.

---

## Files

| File | Purpose |
| ---- | ------- |
| `plugin.yaml` | Plugin manifest (name, parameters, language) |
| `index.ts` | Plugin implementation |
| `authorize.ts` | One-time OAuth2 authorization helper |
| `package.json` | Node.js dependencies |
| `.env.example` | Template for credentials |
| `.env` | Your actual credentials (git-ignored) |
| `token.json` | Saved OAuth2 token (git-ignored, created by authorize.ts) |

---

## Troubleshooting

| Problem | Solution |
| ------- | -------- |
| `Missing GMAIL_CLIENT_ID or GMAIL_CLIENT_SECRET` | Check your `.env` file has the values filled in |
| `No token.json found` | Run `npx tsx authorize.ts` to complete the OAuth flow |
| `invalid_grant` error | Your token expired or was revoked. Delete `token.json` and re-run `authorize.ts` |
| `Access Not Configured` | Make sure the Gmail API is enabled in your Google Cloud project |
| `Error 403: access_denied` | Add your Google account as a test user in the OAuth consent screen |
