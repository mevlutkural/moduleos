import { Injectable } from '@nestjs/common';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import { App, AppId, type AppRepository } from '../../../domain';
import { AppOrmEntity } from '../entities/app.orm-entity';
import { AppEnvVarOrmEntity } from '../entities/app-env-var.orm-entity';
import { AppPersistenceMapper } from '../mappers/app-persistence.mapper';

@Injectable()
export class TypeOrmAppRepository implements AppRepository {
  constructor(
    @InjectRepository(AppOrmEntity)
    private readonly repository: Repository<AppOrmEntity>,
    @InjectRepository(AppEnvVarOrmEntity)
    private readonly envVarRepository: Repository<AppEnvVarOrmEntity>,
  ) {}

  async findById(id: AppId): Promise<App | null> {
    const entity = await this.repository.findOne({
      where: { id: id.getValue() },
    });

    if (!entity) return null;

    return AppPersistenceMapper.toDomain(entity);
  }

  async findByProjectId(projectId: string): Promise<App[]> {
    const entities = await this.repository.find({
      where: { projectId },
    });

    return entities.map((e) => AppPersistenceMapper.toDomain(e));
  }

  async findByName(name: string, projectId: string): Promise<App | null> {
    const entity = await this.repository.findOne({
      where: { name, projectId },
    });

    if (!entity) return null;

    return AppPersistenceMapper.toDomain(entity);
  }

  async save(aggregate: App): Promise<void> {
    await this.envVarRepository.delete({ appId: aggregate.getId().getValue() });

    const entity = AppPersistenceMapper.toPersistence(aggregate);
    await this.repository.save(entity);
  }

  async delete(id: AppId): Promise<void> {
    await this.repository.delete({ id: id.getValue() });
  }

  async exists(id: AppId): Promise<boolean> {
    const count = await this.repository.count({
      where: { id: id.getValue() },
    });
    return count > 0;
  }
}
