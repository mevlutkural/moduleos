import {
  Injectable,
  Inject,
  Logger,
  OnModuleInit,
  OnModuleDestroy,
} from '@nestjs/common';
import Docker from 'dockerode';
import { EventBus } from '@nestjs/cqrs';
import { DOCKER_CLIENT } from '@/shared/infrastructure/docker/docker-client.provider';
import {
  type AppRepository,
  APP_REPOSITORY,
  AppId,
  AppStatusEnum,
} from '../../domain';
import { determineAppStatus } from './docker-status.util';

@Injectable()
export class DockerEventListenerService
  implements OnModuleInit, OnModuleDestroy
{
  private readonly logger = new Logger(DockerEventListenerService.name);
  private eventStream: NodeJS.ReadableStream | null = null;
  private isListening = false;

  constructor(
    @Inject(DOCKER_CLIENT)
    private readonly docker: Docker,
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly eventBus: EventBus,
  ) {}

  onModuleInit(): void {
    void this.startListening();
  }

  onModuleDestroy(): void {
    this.stopListening();
  }

  private async startListening(): Promise<void> {
    if (this.isListening) return;

    try {
      this.logger.log('Starting Docker event stream listener...');

      this.eventStream = (await this.docker.getEvents({
        filters: {
          type: ['service'],
          label: ['moduleos.managed=true'],
        },
      })) as unknown as NodeJS.ReadableStream;

      this.isListening = true;

      this.eventStream.on('data', (chunk: Buffer) => {
        try {
          const event = JSON.parse(chunk.toString()) as {
            Action: string;
            Actor: { Attributes: Record<string, string> };
          };
          void this.handleEvent(event);
        } catch {
          this.logger.warn('Failed to parse Docker event');
        }
      });

      this.eventStream.on('error', (error: Error) => {
        this.logger.error(`Docker event stream error: ${error.message}`);
        this.isListening = false;
        setTimeout(() => void this.startListening(), 5000);
      });

      this.eventStream.on('end', () => {
        this.logger.warn('Docker event stream ended, reconnecting...');
        this.isListening = false;
        setTimeout(() => void this.startListening(), 5000);
      });
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(`Failed to start Docker event listener: ${message}`);
      setTimeout(() => void this.startListening(), 10000);
    }
  }

  private stopListening(): void {
    if (this.eventStream) {
      (
        this.eventStream as NodeJS.ReadableStream & { destroy?: () => void }
      ).destroy?.();
      this.eventStream = null;
      this.isListening = false;
      this.logger.log('Docker event stream listener stopped');
    }
  }

  private async handleEvent(event: {
    Action: string;
    Actor: { Attributes: Record<string, string> };
  }): Promise<void> {
    const appId = event.Actor?.Attributes?.['moduleos.app.id'];
    if (!appId) return;

    this.logger.debug(`Docker event: ${event.Action} for app ${appId}`);

    switch (event.Action) {
      case 'update':
        await this.syncAppStatus(appId);
        break;
      case 'remove':
        await this.markAppAsRemoved(appId);
        break;
    }
  }

  private async syncAppStatus(appIdStr: string): Promise<void> {
    try {
      const app = await this.appRepository.findById(AppId.fromString(appIdStr));
      if (!app?.getSwarmServiceId()) return;

      const service = this.docker.getService(app.getSwarmServiceId()!);
      const inspectData = await service.inspect();

      const tasks: Array<{ Status?: { State?: string } }> =
        await this.docker.listTasks({
          filters: { service: [inspectData.Spec.Name] },
        });

      const desiredReplicas: number =
        inspectData.Spec.Mode?.Replicated?.Replicas ?? 0;
      const newStatus = determineAppStatus(tasks, desiredReplicas);

      if (newStatus !== app.getStatus().value) {
        this.logger.log(
          `Updating app ${appIdStr} status: ${app.getStatus().value} → ${newStatus}`,
        );
        app.updateStatus(newStatus);
        await this.appRepository.save(app);
        this.eventBus.publishAll(app.pullDomainEvents());
      }
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(
        `Failed to sync status for app ${appIdStr}: ${message}`,
      );
    }
  }

  private async markAppAsRemoved(appIdStr: string): Promise<void> {
    try {
      const app = await this.appRepository.findById(AppId.fromString(appIdStr));
      if (!app) return;

      this.logger.log(
        `Marking app ${appIdStr} as stopped (service removed externally)`,
      );
      app.updateStatus(AppStatusEnum.STOPPED);
      await this.appRepository.save(app);
      this.eventBus.publishAll(app.pullDomainEvents());
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(
        `Failed to mark app ${appIdStr} as removed: ${message}`,
      );
    }
  }
}
