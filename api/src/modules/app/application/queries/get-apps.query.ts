import { IQuery } from '@nestjs/cqrs';
import { QueryParams } from '@/shared/application/query';

export class GetAppsQuery implements IQuery {
  constructor(
    public readonly projectId: string,
    public readonly params: QueryParams,
  ) {}
}
