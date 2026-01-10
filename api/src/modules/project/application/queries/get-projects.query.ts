import { IQuery } from '@nestjs/cqrs';
import { QueryParams } from '@/shared/application/query';

export class GetProjectsQuery implements IQuery {
  constructor(public readonly params: QueryParams) {}
}
