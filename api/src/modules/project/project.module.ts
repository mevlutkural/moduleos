import { Module } from '@nestjs/common';
import { TypeOrmModule } from '@nestjs/typeorm';
import { CqrsModule } from '@nestjs/cqrs';
import { ProjectController } from './presentation/controllers/project.controller';
import { ProjectOrmEntity } from './infrastructure/persistence/entities/project.orm-entity';
import { TypeOrmProjectRepository } from './infrastructure/persistence/repositories/typeorm-project.repository';
import { TypeOrmProjectQueryRepository } from './infrastructure/persistence/repositories/typeorm-project-query.repository';
import { PROJECT_REPOSITORY } from './domain/repositories/project.repository';
import { PROJECT_QUERY_REPOSITORY } from './application/queries/repositories/project-query.repository';
import { CreateProjectHandler } from './application/commands/create-project.handler';
import { UpdateProjectHandler } from './application/commands/update-project.handler';
import { DeleteProjectHandler } from './application/commands/delete-project.handler';
import { GetProjectHandler } from './application/queries/get-project.handler';
import { GetProjectsHandler } from './application/queries/get-projects.handler';
import { ProjectResponseMapper } from './application/mappers/project-response.mapper';

const CommandHandlers = [
  CreateProjectHandler,
  UpdateProjectHandler,
  DeleteProjectHandler,
];

const QueryHandlers = [GetProjectHandler, GetProjectsHandler];

@Module({
  imports: [TypeOrmModule.forFeature([ProjectOrmEntity]), CqrsModule],
  controllers: [ProjectController],
  providers: [
    ...CommandHandlers,
    ...QueryHandlers,
    ProjectResponseMapper,
    {
      provide: PROJECT_REPOSITORY,
      useClass: TypeOrmProjectRepository,
    },
    {
      provide: PROJECT_QUERY_REPOSITORY,
      useClass: TypeOrmProjectQueryRepository,
    },
  ],
  exports: [PROJECT_REPOSITORY],
})
export class ProjectModule {}
