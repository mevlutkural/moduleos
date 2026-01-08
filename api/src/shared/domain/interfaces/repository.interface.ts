export interface Repository<TAggregate, TId> {
  findById(id: TId): Promise<TAggregate | null>;

  save(aggregate: TAggregate): Promise<void>;

  delete(id: TId): Promise<void>;

  exists(id: TId): Promise<boolean>;
}
