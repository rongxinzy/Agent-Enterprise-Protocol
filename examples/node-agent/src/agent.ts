import {randomUUID} from 'node:crypto';

import {AepClient, type ControlEvent, type JsonObject} from '@aep/sdk-node';

import {SkillReconciler} from './skills.js';
import {AgentState} from './state.js';

export interface ExampleAgentOptions {
  client: AepClient;
  state: AgentState;
  reconciler: SkillReconciler;
  credentials: {deploymentId: string; username: string; password: string};
}

export class ExampleAgent {
  constructor(private readonly options: ExampleAgentOptions) {}

  async runOnce(): Promise<void> {
    // Keep the same user session across process restarts so its delivery
    // cursor and acknowledgement state remain addressable.
    const restored = await this.options.client.restoreSession();
    if (!restored) await this.options.client.loginWithPassword(this.options.credentials);
    await this.flushTelemetry();
    await this.resumeInbox();
    const skills = this.options.state.managedSkills();
    const heartbeat = await this.options.client.heartbeatUser({
      appliedSkillRevision: this.options.state.getValue('skill_revision'),
      installedSkillIds: skills.map(skill => skill.skillId),
    });
    if (heartbeat.hasPendingControlEvents) await this.receiveControlEvents();
    await this.resumeInbox();
    await this.flushTelemetry();
  }

  async reconcileSkills(): Promise<void> {
    const result = await this.options.reconciler.reconcile();
    await this.options.client.reportSkillSyncResult({revision: result.revision, status: 'succeeded', items: result.items});
  }

  private async receiveControlEvents(): Promise<void> {
    let cursor: string | undefined;
    do {
      const page = await this.options.client.listUserControlEvents(cursor);
      for (const event of page.items) {
        this.options.state.persistInbox(event);
        await this.options.client.acknowledgeUserControlEvent(event.deliveryId, new Date().toISOString());
      }
      cursor = page.nextCursor ?? undefined;
    } while (cursor);
  }

  private async resumeInbox(): Promise<void> {
    for (const item of this.options.state.listPendingInbox()) {
      await this.options.client.acknowledgeUserControlEvent(item.deliveryId, new Date().toISOString());
      await this.execute(item.event);
    }
  }

  private async execute(event: ControlEvent): Promise<void> {
    this.options.state.setInboxState(event.deliveryId, 'running');
    await this.options.client.reportUserControlEventResult(event.deliveryId, {status: 'running', startedAt: new Date().toISOString()});
      try {
      let appliedRevision: string | undefined;
      if (event.task.type === 'skill.reconcile') {
        const result = await this.options.reconciler.reconcile();
        appliedRevision = result.revision;
        await this.options.client.reportSkillSyncResult({revision: result.revision, status: 'succeeded', items: result.items});
      } else {
        throw new Error(`Unsupported M0 task: ${event.task.type}`);
      }
      await this.options.client.reportUserControlEventResult(event.deliveryId, {status: 'succeeded', completedAt: new Date().toISOString(), appliedRevision: appliedRevision ?? ''});
      this.options.state.setInboxState(event.deliveryId, 'succeeded');
      this.queueTelemetry(event, 'succeeded');
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      await this.options.client.reportUserControlEventResult(event.deliveryId, {status: 'failed', completedAt: new Date().toISOString(), errorCode: 'TASK_FAILED', message, retryable: true});
      this.options.state.setInboxState(event.deliveryId, 'failed');
      this.queueTelemetry(event, 'failed', message);
    }
  }

  private queueTelemetry(event: ControlEvent, result: string, message?: string): void {
    this.options.state.enqueueTelemetry({
      eventId: randomUUID(),
      type: result === 'succeeded' ? 'skill.sync.completed' : 'skill.sync.failed',
      occurredAt: new Date().toISOString(),
      resource: event.resource ? {type: event.resource.type, id: event.resource.id} : null,
      result,
      data: message ? {message} : {},
    });
  }

  async flushTelemetry(): Promise<void> {
    const events = this.options.state.listTelemetry();
    if (events.length === 0) return;
    const result = (await this.options.client.uploadUserEventBatch(events)) as {accepted?: unknown};
    const accepted = Array.isArray(result.accepted) ? result.accepted.map(String) : [];
    this.options.state.removeTelemetry(accepted);
  }
}
