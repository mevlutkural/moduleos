import { IQueryHandler, QueryHandler } from '@nestjs/cqrs';
import { Inject, NotFoundException } from '@nestjs/common';
import { GetProjectQuery } from './get-project.query';
import { ProjectDetailProjection } from './projections/project-detail.projection';
import {
  type ProjectQueryRepository,
  PROJECT_QUERY_REPOSITORY,
} from './repositories/project-query.repository';
import { TranslatableMessage } from '@/shared/domain/primitives/translatable-message';

@QueryHandler(GetProjectQuery)
export class GetProjectHandler implements IQueryHandler<GetProjectQuery> {
  constructor(
    @Inject(PROJECT_QUERY_REPOSITORY)
    private readonly queryRepository: ProjectQueryRepository,
  ) {}

  async execute(query: GetProjectQuery): Promise<ProjectDetailProjection> {
    const project = await this.queryRepository.findById(query.id);

    if (!project)
      throw new NotFoundException(
        TranslatableMessage.fromKey('errorMessages.project.notFound', {
          id: query.id,
        }),
      );

    return project;
  }
}
