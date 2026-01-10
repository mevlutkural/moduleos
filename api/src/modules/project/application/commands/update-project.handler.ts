import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import {
  ProjectId,
  ProjectNotFoundException,
  type ProjectRepository,
  PROJECT_REPOSITORY,
} from '../../domain';
import { UpdateProjectCommand } from './update-project.command';
import { ProjectResponseMapper } from '../mappers/project-response.mapper';
import { ProjectResponseDto } from '../dto/project-response.dto';

@CommandHandler(UpdateProjectCommand)
export class UpdateProjectHandler implements ICommandHandler<UpdateProjectCommand> {
  constructor(
    @Inject(PROJECT_REPOSITORY)
    private readonly projectRepository: ProjectRepository,
    private readonly mapper: ProjectResponseMapper,
    private readonly eventBus: EventBus,
  ) {}

  async execute(command: UpdateProjectCommand): Promise<ProjectResponseDto> {
    const projectId = ProjectId.fromString(command.id);
    const project = await this.projectRepository.findById(projectId);

    if (!project) throw new ProjectNotFoundException(command.id);

    project.update(command.name, command.description);
    await this.projectRepository.save(project);

    this.eventBus.publishAll(project.pullDomainEvents());

    return this.mapper.toResponse(project);
  }
}
