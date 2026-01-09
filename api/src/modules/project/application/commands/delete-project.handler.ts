import { CommandHandler, ICommandHandler } from '@nestjs/cqrs';
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
  ) {}

  async execute(command: DeleteProjectCommand): Promise<void> {
    const projectId = ProjectId.fromString(command.id);
    const exists = await this.projectRepository.exists(projectId);

    if (!exists) throw new ProjectNotFoundException(command.id);

    await this.projectRepository.delete(projectId);
  }
}
