import { DomainEvent } from '@/shared/domain';

export class AppCreatedEvent extends DomainEvent {
  constructor(
    public readonly appId: string,
    public readonly name: string,
    public readonly projectId: string,
    public readonly createdAt: Date,
  ) {
    super();
  }

  getEventName(): string {
    return AppCreatedEvent.name;
  }
}
