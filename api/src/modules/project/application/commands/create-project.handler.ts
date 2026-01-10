import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import {
  Project,
  type ProjectRepository,
  PROJECT_REPOSITORY,
} from '../../domain';
import { CreateProjectCommand } from './create-project.command';
import { ProjectResponseMapper } from '../mappers/project-response.mapper';
import { ProjectResponseDto } from '../dto/project-response.dto';

@CommandHandler(CreateProjectCommand)
export class CreateProjectHandler implements ICommandHandler<CreateProjectCommand> {
  constructor(
    @Inject(PROJECT_REPOSITORY)
    private readonly projectRepository: ProjectRepository,
    private readonly mapper: ProjectResponseMapper,
    private readonly eventBus: EventBus,
  ) {}

  async execute(command: CreateProjectCommand): Promise<ProjectResponseDto> {
    const project = Project.create(command.name, command.description);

    await this.projectRepository.save(project);

    this.eventBus.publishAll(project.pullDomainEvents());

    return this.mapper.toResponse(project);
  }
}
