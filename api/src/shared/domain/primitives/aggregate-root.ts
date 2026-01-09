import { Entity } from './entity';
import { DomainEvent } from './domain-event';

export abstract class AggregateRoot<TId> extends Entity<TId> {
  private domainEvents: DomainEvent[] = [];

  protected constructor(id: TId) {
    super(id);
  }

  protected addDomainEvent(event: DomainEvent): void {
    this.domainEvents.push(event);
  }

  public pullDomainEvents(): DomainEvent[] {
    const events = this.domainEvents;
    this.domainEvents = [];
    return events;
  }

  public clearDomainEvents(): void {
    this.domainEvents = [];
  }

  public hasDomainEvents(): boolean {
    return this.domainEvents.length > 0;
  }
}
