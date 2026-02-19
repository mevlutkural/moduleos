import { Repository } from '@/shared/domain';
import { App } from '../entities/app.entity';
import { AppId } from '../value-objects/app.id';

export const APP_REPOSITORY = Symbol('APP_REPOSITORY');

export interface AppRepository extends Repository<App, AppId> {
  findByProjectId(projectId: string): Promise<App[]>;
}
