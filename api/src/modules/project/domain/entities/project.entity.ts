import { AggregateRoot } from '@/shared/domain';
import { ProjectId } from '../value-objects/project.id';
import { ProjectName } from '../value-objects/project-name.value-object';
import { ProjectDescription } from '../value-objects/project-description.value-object';
import {
  ProjectCreatedEvent,
  ProjectUpdatedEvent,
  ProjectDeletedEvent,
} from '../events';

export class Project extends AggregateRoot<ProjectId> {
  private constructor(
    id: ProjectId,
    public name: ProjectName,
    public description: ProjectDescription,
    public createdAt: Date,
    public updatedAt: Date,
  ) {
    super(id);
  }

  static create(name: string, description?: string): Project {
    const now = new Date();
    const id = ProjectId.create();
    const project = new Project(
      id,
      ProjectName.create(name),
      ProjectDescription.create(description ?? null),
      now,
      now,
    );

    project.addDomainEvent(
      new ProjectCreatedEvent(
        id.getValue(),
        project.name.value,
        project.description.value,
        now,
      ),
    );

    return project;
  }

  static reconstitute(
    id: string,
    name: string,
    description: string | null,
    createdAt: Date,
    updatedAt: Date,
  ): Project {
    return new Project(
      ProjectId.fromString(id),
      ProjectName.create(name),
      ProjectDescription.create(description),
      createdAt,
      updatedAt,
    );
  }

  update(name: string, description?: string | null): void {
    this.name = ProjectName.create(name);
    this.description = ProjectDescription.create(description ?? null);
    this.updatedAt = new Date();

    this.addDomainEvent(
      new ProjectUpdatedEvent(
        this.getId().getValue(),
        this.name.value,
        this.description.value,
        this.updatedAt,
      ),
    );
  }

  delete(): void {
    this.addDomainEvent(new ProjectDeletedEvent(this.getId().getValue()));
  }
}
