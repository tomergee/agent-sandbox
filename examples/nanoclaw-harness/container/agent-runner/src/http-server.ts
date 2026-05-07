import { formatMessages, extractRouting, stripInternalTags, type RoutingContext } from './formatter.js';
import { findByName, getAllDestinations, type DestinationEntry } from './destinations.js';
import type { AgentProvider, AgentQuery } from './providers/types.js';

function log(msg: string): void {
  console.error(`[http-server] ${msg}`);
}

function generateId(): string {
  return `msg-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
}

export interface ServerConfig {
  provider: AgentProvider;
  providerName: string;
  cwd: string;
  systemContext?: {
    instructions?: string;
  };
}

let activeQuery: AgentQuery | null = null;
let continuation: string | undefined = undefined;
let currentRouting: RoutingContext | null = null;

export function startServer(config: ServerConfig) {
  const port = 3000;

  log(`Starting agent-runner HTTP server on port ${port}`);

  Bun.serve({
    port: port,
    async fetch(req) {
      const url = new URL(req.url);
      
      if (req.method === 'POST' && url.pathname === '/message') {
        const msg = await req.json();
        log(`Received message: ${msg.id}`);

        // We wrap the single message in an array to reuse existing formatter logic
        const messages = [msg];
        const routing = extractRouting(messages);
        currentRouting = routing; // Save for callbacks

        const prompt = formatMessages(messages);

        if (activeQuery) {
          log('Pushing follow-up message into active query');
          activeQuery.push(prompt);
          return new Response('Pushed', { status: 202 });
        }

        log('Starting new query');
        activeQuery = config.provider.query({
          prompt,
          continuation,
          cwd: config.cwd,
          systemContext: config.systemContext,
        });

        // Process query in background
        (async () => {
          try {
            for await (const event of activeQuery.events) {
              if (event.type === 'init') {
                continuation = event.continuation;
                log(`Session initialized: ${continuation}`);
              } else if (event.type === 'result') {
                log('Result received');
                if (event.text) {
                  await dispatchResultText(event.text, routing);
                }
              }
            }
          } catch (err) {
            log(`Query error: ${err}`);
            await sendCallbackToHost({
              sessionId: process.env.SESSION_ID!,
              message: {
                id: generateId(),
                kind: 'chat',
                platform_id: routing.platformId,
                channel_type: routing.channelType,
                thread_id: routing.threadId,
                content: JSON.stringify({ text: `Error: ${err}` }),
              }
            });
          } finally {
            activeQuery = null;
            log('Query finished');
          }
        })();

        return new Response('Accepted', { status: 202 });
      }
      
      return new Response('Not Found', { status: 404 });
    },
  });
}

async function dispatchResultText(text: string, routing: RoutingContext) {
  const MESSAGE_RE = /<message\s+to="([^"]+)"\s*>([\s\S]*?)<\/message>/g;

  let match: RegExpExecArray | null;
  let sent = 0;
  let lastIndex = 0;
  const scratchpadParts: string[] = [];

  while ((match = MESSAGE_RE.exec(text)) !== null) {
    if (match.index > lastIndex) {
      scratchpadParts.push(text.slice(lastIndex, match.index));
    }
    const toName = match[1];
    const body = match[2].trim();
    lastIndex = MESSAGE_RE.lastIndex;

    const dest = findByName(toName);
    if (!dest) {
      log(`Unknown destination in <message to="${toName}">, dropping block`);
      scratchpadParts.push(`[dropped: unknown destination "${toName}"] ${body}`);
      continue;
    }
    await sendToDestination(dest, body, routing);
    sent++;
  }
  if (lastIndex < text.length) {
    scratchpadParts.push(text.slice(lastIndex));
  }

  const scratchpad = stripInternalTags(scratchpadParts.join(''));

  if (sent === 0 && scratchpad) {
    if (routing.channelType && routing.platformId) {
      await sendCallbackToHost({
        sessionId: process.env.SESSION_ID!,
        message: {
          id: generateId(),
          in_reply_to: routing.inReplyTo,
          kind: 'chat',
          platform_id: routing.platformId,
          channel_type: routing.channelType,
          thread_id: routing.threadId,
          content: JSON.stringify({ text: scratchpad }),
        }
      });
      return;
    }
    const all = getAllDestinations();
    if (all.length === 1) {
      await sendToDestination(all[0], scratchpad, routing);
      return;
    }
  }

  if (scratchpad) {
    log(`[scratchpad] ${scratchpad.slice(0, 500)}${scratchpad.length > 500 ? '…' : ''}`);
  }
}

async function sendToDestination(dest: DestinationEntry, body: string, routing: RoutingContext) {
  const platformId = dest.type === 'channel' ? dest.platformId! : dest.agentGroupId!;
  const channelType = dest.type === 'channel' ? dest.channelType! : 'agent';
  
  await sendCallbackToHost({
    sessionId: process.env.SESSION_ID!,
    message: {
      id: generateId(),
      in_reply_to: routing.inReplyTo,
      kind: 'chat',
      platform_id: platformId,
      channel_type: channelType,
      thread_id: routing.threadId,
      content: JSON.stringify({ text: body }),
    }
  });
}

async function sendCallbackToHost(payload: { sessionId: string, message: any }) {
  const callbackUrl = process.env.HOST_CALLBACK_URL;
  if (!callbackUrl) {
    log('HOST_CALLBACK_URL not set, cannot send callback');
    return;
  }

  log(`Sending callback to host: ${callbackUrl}`);
  try {
    const response = await fetch(callbackUrl, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!response.ok) {
      log(`Host callback failed: ${response.status}`);
    }
  } catch (err) {
    log(`Failed to send callback to host: ${err}`);
  }
}
