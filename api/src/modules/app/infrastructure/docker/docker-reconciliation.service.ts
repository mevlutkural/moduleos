import { Injectable, Inject, Logger } from '@nestjs/common';
import { Interval } from '@nestjs/schedule';
import Docker from 'dockerode';
import { EventBus } from '@nestjs/cqrs';
import { DOCKER_CLIENT } from '@/shared/infrastructure/docker/docker-client.provider';
import {
  type AppRepository,
  APP_REPOSITORY,
  App,
  AppId,
  AppStatusEnum,
} from '../../domain';
import { determineAppStatus } from './docker-status.util';

@Injectable()
export class DockerReconciliationService {
  private readonly logger = new Logger(DockerReconciliationService.name);

  constructor(
    @Inject(DOCKER_CLIENT)
    private readonly docker: Docker,
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly eventBus: EventBus,
  ) {}

  @Interval(30000)
  async reconcile(): Promise<void> {
    try {
      const apps = await this.getActiveApps();

      if (apps.length === 0) return;

      this.logger.debug(`Reconciling ${apps.length} app(s) with Docker Swarm`);

      for (const app of apps) {
        await this.reconcileApp(app);
      }
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(`Reconciliation failed: ${message}`);
    }
  }

  private async getActiveApps(): Promise<App[]> {
    try {
      const managedServices: Array<{
        Spec?: { Labels?: Record<string, string> };
      }> = await this.docker.listServices({
        filters: { label: ['moduleos.managed=true'] },
      });

      const apps: App[] = [];
      for (const svc of managedServices) {
        const appIdStr = svc.Spec?.Labels?.['moduleos.app.id'];
        if (!appIdStr) continue;

        const app = await this.appRepository.findById(
          AppId.fromString(appIdStr),
        );
        if (app?.getSwarmServiceId()) {
          apps.push(app);
        }
      }

      return apps;
    } catch {
      return [];
    }
  }

  private async reconcileApp(app: App): Promise<void> {
    try {
      const serviceId = app.getSwarmServiceId()!;
      const service = this.docker.getService(serviceId);
      const inspectData = await service.inspect();

      const tasks: Array<{ Status?: { State?: string } }> =
        await this.docker.listTasks({
          filters: { service: [inspectData.Spec.Name] },
        });

      const desiredReplicas: number =
        inspectData.Spec.Mode?.Replicated?.Replicas ?? 0;
      const expectedStatus = determineAppStatus(tasks, desiredReplicas);
      const currentStatus = app.getStatus().value;

      if (expectedStatus !== currentStatus) {
        this.logger.log(
          `Drift detected for app ${app.getId().getValue()}: DB=${currentStatus}, Swarm=${expectedStatus}`,
        );
        app.updateStatus(expectedStatus);
        await this.appRepository.save(app);
        this.eventBus.publishAll(app.pullDomainEvents());
      }
    } catch (error: unknown) {
      const statusCode = (error as { statusCode?: number }).statusCode;
      if (statusCode === 404) {
        this.logger.warn(
          `Service ${app.getSwarmServiceId()} not found in Swarm, marking app ${app.getId().getValue()} as stopped`,
        );
        app.updateStatus(AppStatusEnum.STOPPED);
        await this.appRepository.save(app);
        this.eventBus.publishAll(app.pullDomainEvents());
      } else {
        const message = error instanceof Error ? error.message : String(error);
        this.logger.error(
          `Failed to reconcile app ${app.getId().getValue()}: ${message}`,
        );
      }
    }
  }
}
