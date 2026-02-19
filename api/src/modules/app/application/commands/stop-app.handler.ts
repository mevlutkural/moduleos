import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject, Logger } from '@nestjs/common';
import {
  type AppRepository,
  APP_REPOSITORY,
  AppId,
  AppNotFoundException,
} from '../../domain';
import { StopAppCommand } from './stop-app.command';
import {
  type ContainerOrchestrator,
  CONTAINER_ORCHESTRATOR,
} from '../ports/container-orchestrator.port';

@CommandHandler(StopAppCommand)
export class StopAppHandler implements ICommandHandler<StopAppCommand> {
  private readonly logger = new Logger(StopAppHandler.name);

  constructor(
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly eventBus: EventBus,
    @Inject(CONTAINER_ORCHESTRATOR)
    private readonly orchestrator: ContainerOrchestrator,
  ) {}

  async execute(command: StopAppCommand): Promise<void> {
    const appId = AppId.fromString(command.appId);
    const app = await this.appRepository.findById(appId);

    if (!app) {
      throw new AppNotFoundException(command.appId);
    }

    if (app.getSwarmServiceId()) {
      this.logger.log(`Stopping Swarm service for app ${command.appId}`);
      await this.orchestrator.stopService(app.getSwarmServiceId()!);
    }

    app.stop();
    await this.appRepository.save(app);
    this.eventBus.publishAll(app.pullDomainEvents());
  }
}
