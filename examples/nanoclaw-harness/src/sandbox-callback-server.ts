import http from 'http';
import { deliverMessage } from './delivery.js';
import { getSession } from './db/sessions.js';
import { getAgentGroup } from './db/agent-groups.js';
import { openInboundDb } from './session-manager.js';
import { log } from './log.js';

const DEFAULT_PORT = 3001;
let server: http.Server | null = null;

export function startSandboxCallbackServer(): void {
  if (server) return;

  const port = parseInt(process.env.SANDBOX_CALLBACK_PORT || String(DEFAULT_PORT), 10);

  server = http.createServer(async (req, res) => {
    if (req.method !== 'POST' || req.url !== '/callback') {
      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('Not Found');
      return;
    }

    const chunks: Buffer[] = [];
    for await (const chunk of req) {
      chunks.push(chunk as Buffer);
    }
    const body = Buffer.concat(chunks).toString();

    try {
      const payload = JSON.parse(body);
      const { sessionId, message } = payload;

      if (!sessionId || !message) {
        res.writeHead(400, { 'Content-Type': 'text/plain' });
        res.end('Missing sessionId or message');
        return;
      }

      const session = getSession(sessionId);
      if (!session) {
        res.writeHead(404, { 'Content-Type': 'text/plain' });
        res.end(`Session not found: ${sessionId}`);
        return;
      }

      const agentGroup = getAgentGroup(session.agent_group_id);
      if (!agentGroup) {
        res.writeHead(500, { 'Content-Type': 'text/plain' });
        res.end(`Agent group not found for session: ${sessionId}`);
        return;
      }

      const inDb = openInboundDb(agentGroup.id, session.id);
      try {
        await deliverMessage(message, session, inDb);
        res.writeHead(200, { 'Content-Type': 'text/plain' });
        res.end('OK');
      } finally {
        inDb.close();
      }
    } catch (err) {
      log.error('Failed to handle sandbox callback', { err });
      res.writeHead(500, { 'Content-Type': 'text/plain' });
      res.end('Internal Server Error');
    }
  });

  server.listen(port, '0.0.0.0', () => {
    log.info('Sandbox callback server started', { port });
  });
}

export async function stopSandboxCallbackServer(): Promise<void> {
  if (server) {
    await new Promise<void>((resolve) => server!.close(() => resolve()));
    server = null;
    log.info('Sandbox callback server stopped');
  }
}
