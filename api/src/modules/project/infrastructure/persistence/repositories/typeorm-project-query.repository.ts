import { Injectable } from '@nestjs/common';
import { InjectRepository } from '@nestjs/typeorm';
import { Repository } from 'typeorm';
import { I18nService } from 'nestjs-i18n';
import { ProjectQueryRepository } from '../../../application/queries/repositories/project-query.repository';
import { ProjectListProjection } from '../../../application/queries/projections/project-list.projection';
import { ProjectDetailProjection } from '../../../application/queries/projections/project-detail.projection';
import { ProjectOrmEntity } from '../entities/project.orm-entity';
import {
  QueryParams,
  PaginatedResult,
  PaginatedResultBuilder,
} from '@/shared/application/query';
import { TypeOrmQueryApplicator } from '@/shared/infrastructure/persistence/typeorm-query.applicator';

@Injectable()
export class TypeOrmProjectQueryRepository implements ProjectQueryRepository {
  constructor(
    @InjectRepository(ProjectOrmEntity)
    private readonly repository: Repository<ProjectOrmEntity>,
    private readonly i18n: I18nService,
  ) {}

  async findAll(
    params: QueryParams,
  ): Promise<PaginatedResult<ProjectListProjection>> {
    const queryBuilder = this.repository.createQueryBuilder('project');

    new TypeOrmQueryApplicator(this.i18n, queryBuilder, params)
      .applySearch(['name'])
      .applySorting(['name', 'createdAt'])
      .applyPagination();

    const [entities, totalRecords] = await queryBuilder.getManyAndCount();

    const data = entities.map(
      (entity) =>
        new ProjectListProjection(entity.id, entity.name, entity.createdAt),
    );

    return PaginatedResultBuilder.create(
      data,
      totalRecords,
      params.page,
      params.limit,
    );
  }

  async findById(id: string): Promise<ProjectDetailProjection | null> {
    const entity = await this.repository.findOne({
      where: { id },
    });

    if (!entity) return null;

    return new ProjectDetailProjection(
      entity.id,
      entity.name,
      entity.description,
      entity.createdAt,
      entity.updatedAt,
    );
  }
}
