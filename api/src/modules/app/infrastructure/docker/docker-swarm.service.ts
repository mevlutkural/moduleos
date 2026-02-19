import { Injectable, Inject, Logger } from '@nestjs/common';
import Docker from 'dockerode';
import { DOCKER_CLIENT } from '@/shared/infrastructure/docker/docker-client.provider';
import {
  type ContainerOrchestrator,
  type ServiceSpec,
  type ServiceInfo,
} from '../../application/ports/container-orchestrator.port';
import { determineAppStatus } from './docker-status.util';

@Injectable()
export class DockerSwarmService implements ContainerOrchestrator {
  private readonly logger = new Logger(DockerSwarmService.name);

  constructor(
    @Inject(DOCKER_CLIENT)
    private readonly docker: Docker,
  ) {}

  async createService(spec: ServiceSpec): Promise<string> {
    const serviceName = this.buildServiceName(spec.projectId, spec.name);

    this.logger.log(`Creating Swarm service: ${serviceName}`);

    const envVars = spec.envVars.map((ev) => `${ev.key}=${ev.value}`);

    const service = await this.docker.createService({
      Name: serviceName,
      Labels: {
        'moduleos.app.id': spec.appId,
        'moduleos.project.id': spec.projectId,
        'moduleos.managed': 'true',
      },
      TaskTemplate: {
        ContainerSpec: {
          Image: spec.image,
          Env: envVars,
        },
        Resources: {
          Limits: {
            ...(spec.memoryLimit
              ? { MemoryBytes: this.parseMemoryLimit(spec.memoryLimit) }
              : {}),
            ...(spec.cpuLimit
              ? { NanoCPUs: this.parseCpuLimit(spec.cpuLimit) }
              : {}),
          },
        },
        RestartPolicy: {
          Condition: spec.restartPolicy,
          MaxAttempts: 3,
        },
      },
      Mode: {
        Replicated: {
          Replicas: spec.replicas,
        },
      },
      EndpointSpec: {
        Ports: [
          {
            Protocol: 'tcp',
            TargetPort: spec.containerPort,
            PublishMode: 'ingress',
          },
        ],
      },
    });

    const serviceId = service.id;
    this.logger.log(`Swarm service created: ${serviceName} (${serviceId})`);

    return serviceId;
  }

  async updateService(serviceId: string, spec: ServiceSpec): Promise<void> {
    this.logger.log(
      `Updating Swarm service: ${serviceId} (project: ${spec.projectId})`,
    );

    const service = this.docker.getService(serviceId);
    const inspectData = await service.inspect();
    const version = inspectData.Version.Index;

    const envVars = spec.envVars.map((ev) => `${ev.key}=${ev.value}`);

    await service.update({
      version,
      Name: inspectData.Spec.Name,
      Labels: inspectData.Spec.Labels,
      TaskTemplate: {
        ContainerSpec: {
          Image: spec.image,
          Env: envVars,
        },
        Resources: {
          Limits: {
            ...(spec.memoryLimit
              ? { MemoryBytes: this.parseMemoryLimit(spec.memoryLimit) }
              : {}),
            ...(spec.cpuLimit
              ? { NanoCPUs: this.parseCpuLimit(spec.cpuLimit) }
              : {}),
          },
        },
        RestartPolicy: {
          Condition: spec.restartPolicy,
          MaxAttempts: 3,
        },
      },
      Mode: {
        Replicated: {
          Replicas: spec.replicas,
        },
      },
      EndpointSpec: {
        Ports: [
          {
            Protocol: 'tcp',
            TargetPort: spec.containerPort,
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

  async stopService(swarmServiceId: string): Promise<void> {
    this.logger.log(`Stopping Swarm service (scaling to 0): ${swarmServiceId}`);

    const service = this.docker.getService(swarmServiceId);
    const inspectData = await service.inspect();
    const version = inspectData.Version.Index;

    await service.update({
      version,
      Name: inspectData.Spec.Name,
      Labels: inspectData.Spec.Labels,
      TaskTemplate: inspectData.Spec.TaskTemplate,
      Mode: { Replicated: { Replicas: 0 } },
      EndpointSpec: inspectData.Spec.EndpointSpec,
    });

    this.logger.log(`Swarm service stopped: ${swarmServiceId}`);
  }

  async startService(swarmServiceId: string, replicas: number): Promise<void> {
    this.logger.log(
      `Starting Swarm service (scaling to ${replicas}): ${swarmServiceId}`,
    );

    const service = this.docker.getService(swarmServiceId);
    const inspectData = await service.inspect();
    const version = inspectData.Version.Index;

    await service.update({
      version,
      Name: inspectData.Spec.Name,
      Labels: inspectData.Spec.Labels,
      TaskTemplate: inspectData.Spec.TaskTemplate,
      Mode: { Replicated: { Replicas: replicas } },
      EndpointSpec: inspectData.Spec.EndpointSpec,
    });

    this.logger.log(`Swarm service started: ${swarmServiceId}`);
  }

  async getServiceInfo(swarmServiceId: string): Promise<ServiceInfo | null> {
    try {
      const service = this.docker.getService(swarmServiceId);
      const inspectData = await service.inspect();

      const tasks: Array<{ Status?: { State?: string } }> =
        await this.docker.listTasks({
          filters: { service: [inspectData.Spec.Name] },
        });

      const desiredReplicas: number =
        inspectData.Spec.Mode?.Replicated?.Replicas ?? 0;
      const status = determineAppStatus(tasks, desiredReplicas);

      const runningTasks = tasks.filter(
        (t) => t.Status?.State === 'running',
      ).length;

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
