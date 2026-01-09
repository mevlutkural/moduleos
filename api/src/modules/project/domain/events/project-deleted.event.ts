import { DomainEvent } from '@/shared/domain';

export class ProjectDeletedEvent extends DomainEvent {
  constructor(public readonly projectId: string) {
    super();
  }

  getEventName(): string {
    return ProjectDeletedEvent.name;
  }
}
