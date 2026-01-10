import { DomainEvent } from '@/shared/domain';

export class ProjectCreatedEvent extends DomainEvent {
  constructor(
    public readonly projectId: string,
    public readonly name: string,
    public readonly description: string | null,
    public readonly createdAt: Date,
  ) {
    super();
  }

  getEventName(): string {
    return ProjectCreatedEvent.name;
  }
}
