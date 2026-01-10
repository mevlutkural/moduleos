import { Project } from '../../../domain';
import { ProjectOrmEntity } from '../entities/project.orm-entity';

export class ProjectPersistanceMapper {
  static toDomain(entity: ProjectOrmEntity): Project {
    return Project.reconstitute(
      entity.id,
      entity.name,
      entity.description,
      entity.createdAt,
      entity.updatedAt,
    );
  }

  static toPersistence(domain: Project): ProjectOrmEntity {
    const entity = new ProjectOrmEntity();
    entity.id = domain.getId().getValue();
    entity.name = domain.getName().value;
    entity.description = domain.getDescription().value;
    entity.createdAt = domain.getCreatedAt();
    entity.updatedAt = domain.getUpdatedAt();
    return entity;
  }
}
