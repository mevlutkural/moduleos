import {
  Injectable,
  Inject,
  Logger,
  OnModuleInit,
  OnModuleDestroy,
} from '@nestjs/common';
import Docker from 'dockerode';
import { DOCKER_CLIENT } from '@/shared/infrastructure/docker/docker-client.provider';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import { AppOrmEntity } from '../persistence/entities/app.orm-entity';
import { AppStatusEnum } from '../../domain/value-objects/app-status.value-object';

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
    @InjectRepository(AppOrmEntity)
    private readonly appRepository: Repository<AppOrmEntity>,
  ) {}

  async onModuleInit(): Promise<void> {
    await this.startListening();
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
          const event = JSON.parse(chunk.toString());
          this.handleEvent(event);
        } catch {
          this.logger.warn('Failed to parse Docker event');
        }
      });

      this.eventStream.on('error', (error: Error) => {
        this.logger.error(`Docker event stream error: ${error.message}`);
        this.isListening = false;
        setTimeout(() => {
          this.startListening();
        }, 5000);
      });

      this.eventStream.on('end', () => {
        this.logger.warn('Docker event stream ended, reconnecting...');
        this.isListening = false;
        setTimeout(() => {
          this.startListening();
        }, 5000);
      });
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(`Failed to start Docker event listener: ${message}`);
      setTimeout(() => {
        this.startListening();
      }, 10000);
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

  private async syncAppStatus(appId: string): Promise<void> {
    try {
      const app = await this.appRepository.findOne({ where: { id: appId } });
      if (!app || !app.swarmServiceId) return;

      const service = this.docker.getService(app.swarmServiceId);
      const inspectData = await service.inspect();

      const tasks: any[] = await this.docker.listTasks({
        filters: { service: [inspectData.Spec.Name] },
      });

      const runningTasks = tasks.filter(
        (t) => t.Status?.State === 'running',
      ).length;
      const desiredReplicas = inspectData.Spec.Mode?.Replicated?.Replicas ?? 0;

      let newStatus = app.status;
      if (runningTasks === desiredReplicas && desiredReplicas > 0) {
        newStatus = AppStatusEnum.RUNNING;
      } else if (runningTasks > 0) {
        newStatus = AppStatusEnum.DEPLOYING;
      } else {
        const failedTasks = tasks.filter(
          (t) => t.Status?.State === 'failed' || t.Status?.State === 'rejected',
        );
        if (failedTasks.length > 0) {
          newStatus = AppStatusEnum.FAILED;
        }
      }

      if (newStatus !== app.status) {
        this.logger.log(
          `Updating app ${appId} status: ${app.status} → ${newStatus}`,
        );
        await this.appRepository.update({ id: appId }, { status: newStatus });
      }
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(`Failed to sync status for app ${appId}: ${message}`);
    }
  }

  private async markAppAsRemoved(appId: string): Promise<void> {
    try {
      const app = await this.appRepository.findOne({ where: { id: appId } });
      if (!app) return;

      this.logger.log(
        `Marking app ${appId} as stopped (service removed externally)`,
      );
      await this.appRepository.update(
        { id: appId },
        { status: AppStatusEnum.STOPPED, swarmServiceId: null as any },
      );
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(`Failed to mark app ${appId} as removed: ${message}`);
    }
  }
}
