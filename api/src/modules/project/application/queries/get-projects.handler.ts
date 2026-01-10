import { IQueryHandler, QueryHandler } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import { GetProjectsQuery } from './get-projects.query';
import { ProjectListProjection } from './projections/project-list.projection';
import {
  type ProjectQueryRepository,
  PROJECT_QUERY_REPOSITORY,
} from './repositories/project-query.repository';
import { PaginatedResult } from '@/shared/application/query';

@QueryHandler(GetProjectsQuery)
export class GetProjectsHandler implements IQueryHandler<GetProjectsQuery> {
  constructor(
    @Inject(PROJECT_QUERY_REPOSITORY)
    private readonly queryRepository: ProjectQueryRepository,
  ) {}

  async execute(
    query: GetProjectsQuery,
  ): Promise<PaginatedResult<ProjectListProjection>> {
    return this.queryRepository.findAll(query.params);
  }
}
