import { IQueryHandler, QueryHandler } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import { GetAppsQuery } from './get-apps.query';
import { AppListProjection } from './projections/app-list.projection';
import {
  type AppQueryRepository,
  APP_QUERY_REPOSITORY,
} from './repositories/app-query.repository';
import { PaginatedResult } from '@/shared/application/query';

@QueryHandler(GetAppsQuery)
export class GetAppsHandler implements IQueryHandler<GetAppsQuery> {
  constructor(
    @Inject(APP_QUERY_REPOSITORY)
    private readonly queryRepository: AppQueryRepository,
  ) {}

  async execute(
    query: GetAppsQuery,
  ): Promise<PaginatedResult<AppListProjection>> {
    return this.queryRepository.findAllByProjectId(
      query.projectId,
      query.params,
    );
  }
}
