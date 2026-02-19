import { Injectable, Inject, Logger } from '@nestjs/common';
import { Interval } from '@nestjs/schedule';
import Docker from 'dockerode';
import { DOCKER_CLIENT } from '@/shared/infrastructure/docker/docker-client.provider';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository, Not, In } from 'typeorm';
import { AppOrmEntity } from '../persistence/entities/app.orm-entity';
import { AppStatusEnum } from '../../domain/value-objects/app-status.value-object';

@Injectable()
export class DockerReconciliationService {
  private readonly logger = new Logger(DockerReconciliationService.name);

  constructor(
    @Inject(DOCKER_CLIENT)
    private readonly docker: Docker,
    @InjectRepository(AppOrmEntity)
    private readonly appRepository: Repository<AppOrmEntity>,
  ) {}

  @Interval(30000)
  async reconcile(): Promise<void> {
    try {
      const apps = await this.appRepository.find({
        where: {
          swarmServiceId: Not(''),
          status: Not(In([AppStatusEnum.STOPPED])),
        },
      });

      if (apps.length === 0) return;

      this.logger.debug(`Reconciling ${apps.length} app(s) with Docker Swarm`);

      for (const app of apps) {
        if (!app.swarmServiceId) continue;
        await this.reconcileApp(app);
      }
    } catch (error: unknown) {
      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(`Reconciliation failed: ${message}`);
    }
  }

  private async reconcileApp(app: AppOrmEntity): Promise<void> {
    try {
      const service = this.docker.getService(app.swarmServiceId!);
      const inspectData = await service.inspect();

      const tasks: any[] = await this.docker.listTasks({
        filters: { service: [inspectData.Spec.Name] },
      });

      const runningTasks = tasks.filter(
        (t) => t.Status?.State === 'running',
      ).length;
      const desiredReplicas = inspectData.Spec.Mode?.Replicated?.Replicas ?? 0;

      let expectedStatus: AppStatusEnum;
      if (runningTasks === desiredReplicas && desiredReplicas > 0) {
        expectedStatus = AppStatusEnum.RUNNING;
      } else if (runningTasks > 0) {
        expectedStatus = AppStatusEnum.DEPLOYING;
      } else {
        const failedTasks = tasks.filter(
          (t) => t.Status?.State === 'failed' || t.Status?.State === 'rejected',
        );
        expectedStatus =
          failedTasks.length > 0 ? AppStatusEnum.FAILED : AppStatusEnum.CREATED;
      }

      const currentStatus = app.status as AppStatusEnum;

      if (expectedStatus !== currentStatus) {
        this.logger.log(
          `Drift detected for app ${app.id}: DB=${currentStatus}, Swarm=${expectedStatus}`,
        );
        await this.appRepository.update(
          { id: app.id },
          { status: expectedStatus },
        );
      }
    } catch (error: unknown) {
      const statusCode = (error as { statusCode?: number }).statusCode;
      if (statusCode === 404) {
        this.logger.warn(
          `Service ${app.swarmServiceId} not found in Swarm, marking app ${app.id} as stopped`,
        );
        await this.appRepository.update(
          { id: app.id },
          { status: AppStatusEnum.STOPPED, swarmServiceId: null as any },
        );
      } else {
        const message = error instanceof Error ? error.message : String(error);
        this.logger.error(`Failed to reconcile app ${app.id}: ${message}`);
      }
    }
  }
}
