import { Injectable } from '@nestjs/common';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import { I18nService } from 'nestjs-i18n';
import { AppQueryRepository } from '../../../application/queries/repositories/app-query.repository';
import { AppListProjection } from '../../../application/queries/projections/app-list.projection';
import { AppDetailProjection } from '../../../application/queries/projections/app-detail.projection';
import { AppOrmEntity } from '../entities/app.orm-entity';
import {
  QueryParams,
  PaginatedResult,
  PaginatedResultBuilder,
} from '@/shared/application/query';
import { TypeOrmQueryApplicator } from '@/shared/infrastructure/persistence/typeorm-query.applicator';

@Injectable()
export class TypeOrmAppQueryRepository implements AppQueryRepository {
  constructor(
    @InjectRepository(AppOrmEntity)
    private readonly repository: Repository<AppOrmEntity>,
    private readonly i18n: I18nService,
  ) {}

  async findAllByProjectId(
    projectId: string,
    params: QueryParams,
  ): Promise<PaginatedResult<AppListProjection>> {
    const queryBuilder = this.repository
      .createQueryBuilder('app')
      .where('app.project_id = :projectId', { projectId });

    new TypeOrmQueryApplicator(this.i18n, queryBuilder, params)
      .applySearch(['name'])
      .applySorting(['name', 'status', 'createdAt'])
      .applyPagination();

    const [entities, totalRecords] = await queryBuilder.getManyAndCount();

    const data = entities.map(
      (entity) =>
        new AppListProjection(
          entity.id,
          entity.name,
          entity.status,
          entity.replicas,
          entity.createdAt,
        ),
    );

    return PaginatedResultBuilder.create(
      data,
      totalRecords,
      params.page,
      params.limit,
    );
  }

  async findById(id: string): Promise<AppDetailProjection | null> {
    const entity = await this.repository.findOne({
      where: { id },
    });

    if (!entity) return null;

    return new AppDetailProjection(
      entity.id,
      entity.name,
      entity.projectId,
      entity.status,
      entity.containerPort,
      entity.replicas,
      entity.restartPolicy,
      entity.memoryLimit,
      entity.cpuLimit,
      entity.swarmServiceId,
      entity.image,
      (entity.envVars ?? []).map((ev) => ({ key: ev.key, value: ev.value })),
      entity.createdAt,
      entity.updatedAt,
    );
  }
}
