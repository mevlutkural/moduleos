import { DomainEvent } from '@/shared/domain';

export class AppDeletedEvent extends DomainEvent {
  constructor(public readonly appId: string) {
    super();
  }

  getEventName(): string {
    return AppDeletedEvent.name;
  }
}
