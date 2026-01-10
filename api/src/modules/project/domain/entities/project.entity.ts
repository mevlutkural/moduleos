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
    private name: ProjectName,
    private description: ProjectDescription,
    private readonly createdAt: Date,
    private updatedAt: Date,
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

  getName(): ProjectName {
    return this.name;
  }

  getDescription(): ProjectDescription {
    return this.description;
  }

  getCreatedAt(): Date {
    return this.createdAt;
  }

  getUpdatedAt(): Date {
    return this.updatedAt;
  }

  update(name: string, description?: string | null): void {
    const newName = ProjectName.create(name);
    const newDescription = ProjectDescription.create(description ?? null);

    const hasNameChanged = !this.name.equals(newName);
    const hasDescriptionChanged = !this.description.equals(newDescription);

    if (!hasNameChanged && !hasDescriptionChanged) return;

    this.name = newName;
    this.description = newDescription;
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
