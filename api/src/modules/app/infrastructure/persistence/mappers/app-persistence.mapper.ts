import { App, AppStatusEnum } from '../../../domain';
import { AppOrmEntity } from '../entities/app.orm-entity';
import { AppEnvVarOrmEntity } from '../entities/app-env-var.orm-entity';

export class AppPersistenceMapper {
  static toDomain(entity: AppOrmEntity): App {
    return App.reconstitute({
      id: entity.id,
      name: entity.name,
      projectId: entity.projectId,
      status: entity.status as AppStatusEnum,
      containerPort: entity.containerPort,
      replicas: entity.replicas,
      restartPolicy: entity.restartPolicy,
      memoryLimit: entity.memoryLimit,
      cpuLimit: entity.cpuLimit,
      swarmServiceId: entity.swarmServiceId,
      image: entity.image,
      envVars: (entity.envVars ?? []).map((ev) => ({
        key: ev.key,
        value: ev.value,
      })),
      createdAt: entity.createdAt,
      updatedAt: entity.updatedAt,
    });
  }

  static toPersistence(domain: App): AppOrmEntity {
    const entity = new AppOrmEntity();
    entity.id = domain.getId().getValue();
    entity.name = domain.getName().value;
    entity.projectId = domain.getProjectId();
    entity.status = domain.getStatus().value;
    entity.containerPort = domain.getContainerPort();
    entity.replicas = domain.getReplicas();
    entity.restartPolicy = domain.getRestartPolicy();
    entity.memoryLimit = domain.getMemoryLimit();
    entity.cpuLimit = domain.getCpuLimit();
    entity.swarmServiceId = domain.getSwarmServiceId();
    entity.image = domain.getImage();
    entity.createdAt = domain.getCreatedAt();
    entity.updatedAt = domain.getUpdatedAt();

    entity.envVars = domain.getEnvVars().map((ev) => {
      const envVarEntity = new AppEnvVarOrmEntity();
      envVarEntity.appId = domain.getId().getValue();
      envVarEntity.key = ev.key;
      envVarEntity.value = ev.value;
      return envVarEntity;
    });

    return entity;
  }
}
