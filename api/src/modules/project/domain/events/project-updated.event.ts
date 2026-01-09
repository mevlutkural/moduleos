import { DomainEvent } from '@/shared/domain';

export class ProjectUpdatedEvent extends DomainEvent {
  constructor(
    public readonly projectId: string,
    public readonly name: string,
    public readonly description: string | null,
    public readonly updatedAt: Date,
  ) {
    super();
  }

  getEventName(): string {
    return ProjectUpdatedEvent.name;
  }
}
