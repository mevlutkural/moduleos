import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject, Logger } from '@nestjs/common';
import {
  type AppRepository,
  APP_REPOSITORY,
  AppId,
  AppNotFoundException,
} from '../../domain';
import { RestartAppCommand } from './restart-app.command';
import {
  type ContainerOrchestrator,
  CONTAINER_ORCHESTRATOR,
} from '../ports/container-orchestrator.port';

@CommandHandler(RestartAppCommand)
export class RestartAppHandler implements ICommandHandler<RestartAppCommand> {
  private readonly logger = new Logger(RestartAppHandler.name);

  constructor(
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly eventBus: EventBus,
    @Inject(CONTAINER_ORCHESTRATOR)
    private readonly orchestrator: ContainerOrchestrator,
  ) {}

  async execute(command: RestartAppCommand): Promise<void> {
    const appId = AppId.fromString(command.appId);
    const app = await this.appRepository.findById(appId);

    if (!app) {
      throw new AppNotFoundException(command.appId);
    }

    const serviceId = app.getSwarmServiceId();
    if (!serviceId) {
      this.logger.warn(
        `App ${command.appId} has no Swarm service, cannot restart`,
      );
      return;
    }

    this.logger.log(`Restarting Swarm service for app ${command.appId}`);

    await this.orchestrator.stopService(serviceId);
    await this.orchestrator.startService(serviceId, app.getReplicas());

    app.start();
    await this.appRepository.save(app);
    this.eventBus.publishAll(app.pullDomainEvents());
  }
}
