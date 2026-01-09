import { CommandHandler, ICommandHandler } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import {
  Project,
  ProjectId,
  ProjectNotFoundException,
  type ProjectRepository,
  PROJECT_REPOSITORY,
} from '../../domain';
import { UpdateProjectCommand } from './update-project.command';

@CommandHandler(UpdateProjectCommand)
export class UpdateProjectHandler implements ICommandHandler<UpdateProjectCommand> {
  constructor(
    @Inject(PROJECT_REPOSITORY)
    private readonly projectRepository: ProjectRepository,
  ) {}

  async execute(command: UpdateProjectCommand): Promise<Project> {
    const projectId = ProjectId.fromString(command.id);
    const project = await this.projectRepository.findById(projectId);

    if (!project) throw new ProjectNotFoundException(command.id);

    project.update(command.name, command.description);
    await this.projectRepository.save(project);

    return project;
  }
}
