import { DomainEvent } from '@/shared/domain';

export class AppConfigUpdatedEvent extends DomainEvent {
  constructor(
    public readonly appId: string,
    public readonly updatedFields: string[],
    public readonly updatedAt: Date,
  ) {
    super();
  }

  getEventName(): string {
    return AppConfigUpdatedEvent.name;
  }
}
