import { StreamCoreAIPlugin } from '@streamcore/plugin';
import OpenAI from 'openai';
import * as path from 'path';
import * as fs from 'fs';

// Load .env from the plugin's own directory
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

const VISION_MODEL = process.env.VISION_MODEL || 'gpt-4o-mini';

let openai: OpenAI | null = null;

function getClient(): OpenAI {
  if (!openai) {
    const apiKey = process.env.OPENAI_API_KEY;
    if (!apiKey) {
      throw new Error(
        'OPENAI_API_KEY environment variable is not set. ' +
        'Export it before starting the server, e.g.: export OPENAI_API_KEY=sk-...'
      );
    }
    openai = new OpenAI({ apiKey });
  }
  return openai;
}

plugin.onExecute(async (params: Record<string, unknown>) => {
  const question = (params.question as string) || 'What do you see in this image?';
  const imageBase64 = params.image_base64 as string | undefined;
  const imageMime = (params.image_mime as string) || 'image/jpeg';

  if (!imageBase64) {
    return 'No image was received from the device camera. Please ask the user to try again.';
  }

  const client = getClient();
  const dataUrl = `data:${imageMime};base64,${imageBase64}`;

  console.error(`[vision] model=${VISION_MODEL} base64_len=${imageBase64.length} mime=${imageMime} question="${question}"`);

  try {
    const response = await client.chat.completions.create({
      model: VISION_MODEL,
      max_tokens: 512,
      messages: [
        {
          role: 'user',
          content: [
            {
              type: 'text',
              text: question,
            },
            {
              type: 'image_url',
              image_url: {
                url: dataUrl,
                detail: 'auto',
              },
            },
          ],
        },
      ],
    });

    const answer = response.choices?.[0]?.message?.content;
    console.error(`[vision] response: ${answer?.substring(0, 120)}`);
    if (!answer) {
      throw new Error('Vision API returned an empty response');
    }

    return answer;
  } catch (err: any) {
    console.error(`[vision] API error: ${err.message}`);
    throw err;
  }
});

plugin.run();
