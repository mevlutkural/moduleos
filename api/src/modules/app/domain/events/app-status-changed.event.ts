import { DomainEvent } from '@/shared/domain';
import { AppStatusEnum } from '../value-objects/app-status.value-object';

export class AppStatusChangedEvent extends DomainEvent {
  constructor(
    public readonly appId: string,
    public readonly oldStatus: AppStatusEnum,
    public readonly newStatus: AppStatusEnum,
  ) {
    super();
  }

  getEventName(): string {
    return AppStatusChangedEvent.name;
  }
}
