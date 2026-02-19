import { Injectable, Inject, Logger } from '@nestjs/common';
import Docker from 'dockerode';
import { DOCKER_CLIENT } from '@/shared/infrastructure/docker/docker-client.provider';
import { App } from '../../domain';
import {
  type ContainerOrchestrator,
  type ServiceInfo,
} from '../../application/ports/container-orchestrator.port';

@Injectable()
export class DockerSwarmService implements ContainerOrchestrator {
  private readonly logger = new Logger(DockerSwarmService.name);

  constructor(
    @Inject(DOCKER_CLIENT)
    private readonly docker: Docker,
  ) {}

  async createService(app: App, projectId: string): Promise<string> {
    const serviceName = this.buildServiceName(projectId, app.getName().value);

    this.logger.log(`Creating Swarm service: ${serviceName}`);

    const envVars = app.getEnvVars().map((ev) => `${ev.key}=${ev.value}`);

    const service = await this.docker.createService({
      Name: serviceName,
      Labels: {
        'moduleos.app.id': app.getId().getValue(),
        'moduleos.project.id': projectId,
        'moduleos.managed': 'true',
      },
      TaskTemplate: {
        ContainerSpec: {
          Image: 'nginx:alpine',
          Env: envVars,
        },
        Resources: {
          Limits: {
            ...(app.getMemoryLimit()
              ? { MemoryBytes: this.parseMemoryLimit(app.getMemoryLimit()!) }
              : {}),
            ...(app.getCpuLimit()
              ? { NanoCPUs: this.parseCpuLimit(app.getCpuLimit()!) }
              : {}),
          },
        },
        RestartPolicy: {
          Condition: app.getRestartPolicy(),
          MaxAttempts: 3,
        },
      },
      Mode: {
        Replicated: {
          Replicas: app.getReplicas(),
        },
      },
      EndpointSpec: {
        Ports: [
          {
            Protocol: 'tcp',
            TargetPort: app.getContainerPort(),
            PublishMode: 'ingress',
          },
        ],
      },
    });

    const serviceId = service.id;
    this.logger.log(`Swarm service created: ${serviceName} (${serviceId})`);

    return serviceId;
  }

  async updateService(app: App, projectId: string): Promise<void> {
    const serviceId = app.getSwarmServiceId();
    if (!serviceId) {
      this.logger.warn(
        `App ${app.getId().getValue()} has no Swarm service ID, skipping update`,
      );
      return;
    }

    this.logger.log(
      `Updating Swarm service: ${serviceId} (project: ${projectId})`,
    );

    const service = this.docker.getService(serviceId);
    const inspectData = await service.inspect();
    const version = inspectData.Version.Index;

    const envVars = app.getEnvVars().map((ev) => `${ev.key}=${ev.value}`);

    await service.update({
      version,
      Name: inspectData.Spec.Name,
      Labels: inspectData.Spec.Labels,
      TaskTemplate: {
        ContainerSpec: {
          Image: inspectData.Spec.TaskTemplate.ContainerSpec.Image,
          Env: envVars,
        },
        Resources: {
          Limits: {
            ...(app.getMemoryLimit()
              ? { MemoryBytes: this.parseMemoryLimit(app.getMemoryLimit()!) }
              : {}),
            ...(app.getCpuLimit()
              ? { NanoCPUs: this.parseCpuLimit(app.getCpuLimit()!) }
              : {}),
          },
        },
        RestartPolicy: {
          Condition: app.getRestartPolicy(),
          MaxAttempts: 3,
        },
      },
      Mode: {
        Replicated: {
          Replicas: app.getReplicas(),
        },
      },
      EndpointSpec: {
        Ports: [
          {
            Protocol: 'tcp',
            TargetPort: app.getContainerPort(),
            PublishMode: 'ingress',
          },
        ],
      },
    });

    this.logger.log(`Swarm service updated: ${serviceId}`);
  }

  async removeService(swarmServiceId: string): Promise<void> {
    this.logger.log(`Removing Swarm service: ${swarmServiceId}`);

    const service = this.docker.getService(swarmServiceId);
    await service.remove();

    this.logger.log(`Swarm service removed: ${swarmServiceId}`);
  }

  async getServiceInfo(swarmServiceId: string): Promise<ServiceInfo | null> {
    try {
      const service = this.docker.getService(swarmServiceId);
      const inspectData = await service.inspect();

      const tasks: any[] = await this.docker.listTasks({
        filters: { service: [inspectData.Spec.Name] },
      });

      const runningTasks = tasks.filter(
        (t) => t.Status?.State === 'running',
      ).length;
      const desiredReplicas = inspectData.Spec.Mode?.Replicated?.Replicas ?? 0;

      let status = 'created';
      if (runningTasks === desiredReplicas && desiredReplicas > 0) {
        status = 'running';
      } else if (runningTasks > 0) {
        status = 'deploying';
      } else {
        const failedTasks = tasks.filter(
          (t) => t.Status?.State === 'failed' || t.Status?.State === 'rejected',
        );
        if (failedTasks.length > 0) {
          status = 'failed';
        }
      }

      return {
        id: swarmServiceId,
        status,
        replicas: {
          running: runningTasks,
          desired: desiredReplicas,
        },
      };
    } catch (error: unknown) {
      const statusCode = (error as { statusCode?: number }).statusCode;
      if (statusCode === 404) {
        return null;
      }
      throw error;
    }
  }

  async listManagedServices(): Promise<any[]> {
    return this.docker.listServices({
      filters: { label: ['moduleos.managed=true'] },
    });
  }

  private buildServiceName(projectId: string, appName: string): string {
    const shortProjectId = projectId.split('-')[0];
    return `moduleos_${shortProjectId}_${appName}`;
  }

  private parseMemoryLimit(limit: string): number {
    const match = /^(\d+)([mg])$/i.exec(limit);
    if (!match) return 0;

    const value = Number.parseInt(match[1], 10);
    const unit = match[2].toLowerCase();

    if (unit === 'g') return value * 1024 * 1024 * 1024;
    if (unit === 'm') return value * 1024 * 1024;
    return 0;
  }

  private parseCpuLimit(limit: string): number {
    const value = Number.parseFloat(limit);
    return Math.round(value * 1e9);
  }
}
