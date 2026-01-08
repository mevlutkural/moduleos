export abstract class Entity<TId> {
  protected constructor(protected readonly id: TId) {}

  getId(): TId {
    return this.id;
  }

  equals(other: Entity<TId>): boolean {
    if (other === null || other === undefined) return false;
    if (this === other) return true;
    if (!(other instanceof Entity)) return false;

    return this.id === other.id;
  }
}
