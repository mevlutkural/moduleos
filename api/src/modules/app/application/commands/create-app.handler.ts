import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject, Logger } from '@nestjs/common';
import {
  App,
  type AppRepository,
  APP_REPOSITORY,
  AppNameAlreadyExistsException,
} from '../../domain';
import {
  type ProjectRepository,
  PROJECT_REPOSITORY,
  ProjectNotFoundException,
} from '@/modules/project/domain';
import { ProjectId } from '@/modules/project/domain/value-objects/project.id';
import { CreateAppCommand } from './create-app.command';
import { AppResponseMapper } from '../mappers/app-response.mapper';
import { AppResponseDto } from '../dto/app-response.dto';
import {
  type ContainerOrchestrator,
  CONTAINER_ORCHESTRATOR,
} from '../ports/container-orchestrator.port';
import { buildServiceSpec } from '../mappers/service-spec.mapper';

@CommandHandler(CreateAppCommand)
export class CreateAppHandler implements ICommandHandler<CreateAppCommand> {
  private readonly logger = new Logger(CreateAppHandler.name);

  constructor(
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    @Inject(PROJECT_REPOSITORY)
    private readonly projectRepository: ProjectRepository,
    private readonly mapper: AppResponseMapper,
    private readonly eventBus: EventBus,
    @Inject(CONTAINER_ORCHESTRATOR)
    private readonly orchestrator: ContainerOrchestrator,
  ) {}

  async execute(command: CreateAppCommand): Promise<AppResponseDto> {
    const projectId = ProjectId.fromString(command.projectId);
    const projectExists = await this.projectRepository.exists(projectId);
    if (!projectExists) {
      throw new ProjectNotFoundException(command.projectId);
    }

    const existing = await this.appRepository.findByName(
      command.name,
      command.projectId,
    );
    if (existing) {
      throw new AppNameAlreadyExistsException(command.name, command.projectId);
    }

    const app = App.create(command.name, command.projectId);

    await this.appRepository.save(app);

    try {
      const swarmServiceId = await this.orchestrator.createService(
        buildServiceSpec(app),
      );
      app.setSwarmServiceId(swarmServiceId);
      await this.appRepository.save(app);
    } catch (error: unknown) {
      this.logger.error(
        `Failed to create Swarm service for app ${app.getId().getValue()}, marking as failed`,
      );
      app.markFailed();
      await this.appRepository.save(app);

      const message = error instanceof Error ? error.message : String(error);
      this.logger.error(`Swarm error: ${message}`);
    }

    this.eventBus.publishAll(app.pullDomainEvents());

    return this.mapper.toResponse(app);
  }
}
