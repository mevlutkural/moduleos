import { CommandHandler, ICommandHandler } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import {
  Project,
  type ProjectRepository,
  PROJECT_REPOSITORY,
} from '../../domain';
import { CreateProjectCommand } from './create-project.command';

@CommandHandler(CreateProjectCommand)
export class CreateProjectHandler implements ICommandHandler<CreateProjectCommand> {
  constructor(
    @Inject(PROJECT_REPOSITORY)
    private readonly projectRepository: ProjectRepository,
  ) {}

  async execute(command: CreateProjectCommand): Promise<Project> {
    const project = Project.create(command.name, command.description);
    await this.projectRepository.save(project);
    return project;
  }
}
