import { ProjectListProjection } from '../projections/project-list.projection';
import { ProjectDetailProjection } from '../projections/project-detail.projection';

export const PROJECT_QUERY_REPOSITORY = Symbol('PROJECT_QUERY_REPOSITORY');

export interface ProjectQueryRepository {
  findAll(): Promise<ProjectListProjection[]>;
  findById(id: string): Promise<ProjectDetailProjection | null>;
}
