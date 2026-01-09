import { Repository } from '@/shared/domain';
import { Project } from '../entities/project.entity';
import { ProjectId } from '../value-objects/project.id';

export const PROJECT_REPOSITORY = Symbol('PROJECT_REPOSITORY');

export interface ProjectRepository extends Repository<Project, ProjectId> {
  findAll(): Promise<Project[]>;
}
