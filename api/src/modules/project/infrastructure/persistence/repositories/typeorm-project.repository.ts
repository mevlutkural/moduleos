import { Injectable } from '@nestjs/common';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import { Project, ProjectId, ProjectRepository } from '../../../domain';
import { ProjectOrmEntity } from '../entities/project.orm-entity';
import { ProjectPersistanceMapper } from '../mappers/project-persistance.mapper';

@Injectable()
export class TypeOrmProjectRepository implements ProjectRepository {
  constructor(
    @InjectRepository(ProjectOrmEntity)
    private readonly repository: Repository<ProjectOrmEntity>,
  ) {}

  async findById(id: ProjectId): Promise<Project | null> {
    const entity = await this.repository.findOne({
      where: { id: id.getValue() },
    });

    if (!entity) return null;

    return ProjectPersistanceMapper.toDomain(entity);
  }

  async save(aggregate: Project): Promise<void> {
    const entity = ProjectPersistanceMapper.toPersistence(aggregate);
    await this.repository.save(entity);
  }

  async delete(id: ProjectId): Promise<void> {
    await this.repository.delete({ id: id.getValue() });
  }

  async exists(id: ProjectId): Promise<boolean> {
    const count = await this.repository.count({
      where: { id: id.getValue() },
    });
    return count > 0;
  }
}
