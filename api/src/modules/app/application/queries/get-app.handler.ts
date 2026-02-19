import { IQueryHandler, QueryHandler } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import { GetAppQuery } from './get-app.query';
import { AppDetailProjection } from './projections/app-detail.projection';
import {
  type AppQueryRepository,
  APP_QUERY_REPOSITORY,
} from './repositories/app-query.repository';
import { AppNotFoundException } from '../../domain';

@QueryHandler(GetAppQuery)
export class GetAppHandler implements IQueryHandler<GetAppQuery> {
  constructor(
    @Inject(APP_QUERY_REPOSITORY)
    private readonly queryRepository: AppQueryRepository,
  ) {}

  async execute(query: GetAppQuery): Promise<AppDetailProjection> {
    const result = await this.queryRepository.findById(query.appId);

    if (!result) {
      throw new AppNotFoundException(query.appId);
    }

    return result;
  }
}
