import { AggregateRoot } from '@/shared/domain';
import { AppId } from '../value-objects/app.id';
import { AppName } from '../value-objects/app-name.value-object';
import {
  AppStatus,
  AppStatusEnum,
} from '../value-objects/app-status.value-object';
import {
  AppCreatedEvent,
  AppConfigUpdatedEvent,
  AppStatusChangedEvent,
  AppDeletedEvent,
} from '../events';

export interface AppConfig {
  containerPort: number;
  replicas: number;
  restartPolicy: string;
  memoryLimit: string | null;
  cpuLimit: string | null;
}

export interface AppEnvVar {
  key: string;
  value: string;
}

export class App extends AggregateRoot<AppId> {
  private constructor(
    id: AppId,
    private readonly name: AppName,
    private readonly projectId: string,
    private status: AppStatus,
    private containerPort: number,
    private replicas: number,
    private restartPolicy: string,
    private memoryLimit: string | null,
    private cpuLimit: string | null,
    private swarmServiceId: string | null,
    private envVars: AppEnvVar[],
    private readonly createdAt: Date,
    private updatedAt: Date,
  ) {
    super(id);
  }

  static create(name: string, projectId: string): App {
    const now = new Date();
    const id = AppId.create();
    const app = new App(
      id,
      AppName.create(name),
      projectId,
      AppStatus.created(),
      80, // default container port
      1, // default replicas
      'on-failure', // default restart policy
      null, // no memory limit
      null, // no cpu limit
      null, // no swarm service id yet
      [], // no env vars
      now,
      now,
    );

    app.addDomainEvent(
      new AppCreatedEvent(id.getValue(), app.name.value, projectId, now),
    );

    return app;
  }

  static reconstitute(props: {
    id: string;
    name: string;
    projectId: string;
    status: AppStatusEnum;
    containerPort: number;
    replicas: number;
    restartPolicy: string;
    memoryLimit: string | null;
    cpuLimit: string | null;
    swarmServiceId: string | null;
    envVars: AppEnvVar[];
    createdAt: Date;
    updatedAt: Date;
  }): App {
    return new App(
      AppId.fromString(props.id),
      AppName.create(props.name),
      props.projectId,
      AppStatus.create(props.status),
      props.containerPort,
      props.replicas,
      props.restartPolicy,
      props.memoryLimit,
      props.cpuLimit,
      props.swarmServiceId,
      props.envVars,
      props.createdAt,
      props.updatedAt,
    );
  }

  getName(): AppName {
    return this.name;
  }

  getProjectId(): string {
    return this.projectId;
  }

  getStatus(): AppStatus {
    return this.status;
  }

  getContainerPort(): number {
    return this.containerPort;
  }

  getReplicas(): number {
    return this.replicas;
  }

  getRestartPolicy(): string {
    return this.restartPolicy;
  }

  getMemoryLimit(): string | null {
    return this.memoryLimit;
  }

  getCpuLimit(): string | null {
    return this.cpuLimit;
  }

  getSwarmServiceId(): string | null {
    return this.swarmServiceId;
  }

  getEnvVars(): AppEnvVar[] {
    return [...this.envVars];
  }

  getCreatedAt(): Date {
    return this.createdAt;
  }

  getUpdatedAt(): Date {
    return this.updatedAt;
  }

  setSwarmServiceId(serviceId: string): void {
    this.swarmServiceId = serviceId;
    this.updatedAt = new Date();
  }

  updateConfig(config: Partial<AppConfig>): void {
    const updatedFields: string[] = [];

    if (
      config.containerPort !== undefined &&
      config.containerPort !== this.containerPort
    ) {
      this.containerPort = config.containerPort;
      updatedFields.push('containerPort');
    }

    if (config.replicas !== undefined && config.replicas !== this.replicas) {
      this.replicas = config.replicas;
      updatedFields.push('replicas');
    }

    if (
      config.restartPolicy !== undefined &&
      config.restartPolicy !== this.restartPolicy
    ) {
      this.restartPolicy = config.restartPolicy;
      updatedFields.push('restartPolicy');
    }

    if (
      config.memoryLimit !== undefined &&
      config.memoryLimit !== this.memoryLimit
    ) {
      this.memoryLimit = config.memoryLimit;
      updatedFields.push('memoryLimit');
    }

    if (config.cpuLimit !== undefined && config.cpuLimit !== this.cpuLimit) {
      this.cpuLimit = config.cpuLimit;
      updatedFields.push('cpuLimit');
    }

    if (updatedFields.length === 0) return;

    this.updatedAt = new Date();

    this.addDomainEvent(
      new AppConfigUpdatedEvent(
        this.getId().getValue(),
        updatedFields,
        this.updatedAt,
      ),
    );
  }

  updateEnvVars(envVars: AppEnvVar[]): void {
    this.envVars = [...envVars];
    this.updatedAt = new Date();

    this.addDomainEvent(
      new AppConfigUpdatedEvent(
        this.getId().getValue(),
        ['envVars'],
        this.updatedAt,
      ),
    );
  }

  updateStatus(newStatus: AppStatusEnum): void {
    const newStatusVO = AppStatus.create(newStatus);
    if (this.status.equals(newStatusVO)) return;

    const oldStatus = this.status.value;
    this.status = newStatusVO;
    this.updatedAt = new Date();

    this.addDomainEvent(
      new AppStatusChangedEvent(this.getId().getValue(), oldStatus, newStatus),
    );
  }

  delete(): void {
    this.addDomainEvent(new AppDeletedEvent(this.getId().getValue()));
  }
}
