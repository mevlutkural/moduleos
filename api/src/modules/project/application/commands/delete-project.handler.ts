import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import {
  ProjectId,
  ProjectNotFoundException,
  type ProjectRepository,
  PROJECT_REPOSITORY,
} from '../../domain';
import { DeleteProjectCommand } from './delete-project.command';

@CommandHandler(DeleteProjectCommand)
export class DeleteProjectHandler implements ICommandHandler<DeleteProjectCommand> {
  constructor(
    @Inject(PROJECT_REPOSITORY)
    private readonly projectRepository: ProjectRepository,
    private readonly eventBus: EventBus,
  ) {}

  async execute(command: DeleteProjectCommand): Promise<void> {
    const projectId = ProjectId.fromString(command.id);
    const project = await this.projectRepository.findById(projectId);

    if (!project) throw new ProjectNotFoundException(command.id);

    project.delete();

    await this.projectRepository.delete(project.getId());

    this.eventBus.publishAll(project.pullDomainEvents());
  }
}
