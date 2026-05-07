import * as k8s from '@kubernetes/client-node';
import { log } from './log.js';

const CLAIM_API_GROUP = 'extensions.agents.x-k8s.io';
const CLAIM_API_VERSION = 'v1alpha1';
const CLAIM_PLURAL_NAME = 'sandboxclaims';

const SANDBOX_API_GROUP = 'agents.x-k8s.io';
const SANDBOX_API_VERSION = 'v1alpha1';
const SANDBOX_PLURAL_NAME = 'sandboxes';

export class AgentSandboxRunner {
  private kc: k8s.KubeConfig;
  private customObjectsApi: k8s.CustomObjectsApi;

  constructor() {
    this.kc = new k8s.KubeConfig();
    this.kc.loadFromDefault();
    this.customObjectsApi = this.kc.makeApiClient(k8s.CustomObjectsApi) as any;
  }

  async createSandboxClaim(args: {
    name: string;
    template: string;
    namespace: string;
    annotations?: Record<string, string>;
    labels?: Record<string, string>;
    lifecycle?: any;
    warmpool?: string;
    env?: { name: string; value: string }[];
  }): Promise<void> {
    const manifest = {
      apiVersion: `${CLAIM_API_GROUP}/${CLAIM_API_VERSION}`,
      kind: 'SandboxClaim',
      metadata: {
        name: args.name,
        annotations: args.annotations || {},
        labels: args.labels || {},
      },
      spec: {
        sandboxTemplateRef: {
          name: args.template,
        },
        lifecycle: args.lifecycle,
        warmpool: args.warmpool,
        env: args.env,
      },
    };

    log.info('Creating SandboxClaim', { name: args.name, namespace: args.namespace, template: args.template });

    try {
      await (this.customObjectsApi as any).createNamespacedCustomObject(
        CLAIM_API_GROUP,
        CLAIM_API_VERSION,
        args.namespace,
        CLAIM_PLURAL_NAME,
        manifest
      );
    } catch (err) {
      log.error('Failed to create SandboxClaim', { name: args.name, err });
      throw err;
    }
  }

  async resolveSandboxName(claimName: string, namespace: string, timeoutSeconds: number): Promise<string> {
    log.info('Resolving sandbox name from claim', { claimName, namespace });
    
    const deadline = Date.now() + timeoutSeconds * 1000;
    
    while (Date.now() < deadline) {
      try {
        const res = await (this.customObjectsApi as any).getNamespacedCustomObject(
          CLAIM_API_GROUP,
          CLAIM_API_VERSION,
          namespace,
          CLAIM_PLURAL_NAME,
          claimName
        );
        
        const status = res.status || {};
        const sandboxStatus = status.sandbox || {};
        const name = sandboxStatus.name || sandboxStatus.Name;
        if (name) {
          log.info('Resolved sandbox name', { name });
          return name;
        }
      } catch (err) {
        log.debug('Error getting claim, retrying', { err });
      }
      await new Promise(resolve => setTimeout(resolve, 5000));
    }
    throw new Error(`Timeout resolving sandbox name for claim ${claimName}`);
  }

  async waitForSandboxReady(name: string, namespace: string, timeoutSeconds: number): Promise<string | undefined> {
    log.info('Waiting for Sandbox to be ready', { name, namespace });
    
    const deadline = Date.now() + timeoutSeconds * 1000;
    
    while (Date.now() < deadline) {
      try {
        const res = await (this.customObjectsApi as any).getNamespacedCustomObject(
          SANDBOX_API_GROUP,
          SANDBOX_API_VERSION,
          namespace,
          SANDBOX_PLURAL_NAME,
          name
        );
        
        const status = res.status || {};
        const conditions = status.conditions || [];
        const readyCond = conditions.find((c: any) => c.type === 'Ready');
        
        if (readyCond && readyCond.status === 'True') {
          log.info('Sandbox is ready', { name });
          const podIPs = status.podIPs || [];
          return podIPs[0];
        }
      } catch (err) {
        log.debug('Error getting sandbox, retrying', { err });
      }
      await new Promise(resolve => setTimeout(resolve, 5000));
    }
    throw new Error(`Timeout waiting for sandbox ${name} to be ready`);
  }

  async deleteSandboxClaim(name: string, namespace: string): Promise<void> {
    log.info('Deleting SandboxClaim', { name, namespace });
    try {
      await (this.customObjectsApi as any).deleteNamespacedCustomObject(
        CLAIM_API_GROUP,
        CLAIM_API_VERSION,
        namespace,
        CLAIM_PLURAL_NAME,
        name
      );
    } catch (err) {
      // Ignore 404
      const apiErr = err as any;
      if (apiErr.response && apiErr.response.statusCode !== 404) {
        log.error('Failed to delete SandboxClaim', { name, err });
        throw err;
      }
    }
  }
}
