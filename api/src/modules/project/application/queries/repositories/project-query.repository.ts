import { ProjectListProjection } from '../projections/project-list.projection';
import { ProjectDetailProjection } from '../projections/project-detail.projection';
import { QueryParams, PaginatedResult } from '@/shared/application/query';

export const PROJECT_QUERY_REPOSITORY = Symbol('PROJECT_QUERY_REPOSITORY');

export interface ProjectQueryRepository {
  findAll(params: QueryParams): Promise<PaginatedResult<ProjectListProjection>>;
  findById(id: string): Promise<ProjectDetailProjection | null>;
}
