import { AppListProjection } from '../projections/app-list.projection';
import { AppDetailProjection } from '../projections/app-detail.projection';
import { QueryParams, PaginatedResult } from '@/shared/application/query';

export const APP_QUERY_REPOSITORY = Symbol('APP_QUERY_REPOSITORY');

export interface AppQueryRepository {
  findAllByProjectId(
    projectId: string,
    params: QueryParams,
  ): Promise<PaginatedResult<AppListProjection>>;
  findById(id: string): Promise<AppDetailProjection | null>;
}
