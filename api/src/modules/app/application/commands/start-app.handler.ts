import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject, Logger } from '@nestjs/common';
import {
  type AppRepository,
  APP_REPOSITORY,
  AppId,
  AppNotFoundException,
} from '../../domain';
import { StartAppCommand } from './start-app.command';
import {
  type ContainerOrchestrator,
  CONTAINER_ORCHESTRATOR,
} from '../ports/container-orchestrator.port';
import { buildServiceSpec } from '../mappers/service-spec.mapper';

@CommandHandler(StartAppCommand)
export class StartAppHandler implements ICommandHandler<StartAppCommand> {
  private readonly logger = new Logger(StartAppHandler.name);

  constructor(
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly eventBus: EventBus,
    @Inject(CONTAINER_ORCHESTRATOR)
    private readonly orchestrator: ContainerOrchestrator,
  ) {}

  async execute(command: StartAppCommand): Promise<void> {
    const appId = AppId.fromString(command.appId);
    const app = await this.appRepository.findById(appId);

    if (!app) {
      throw new AppNotFoundException(command.appId);
    }

    if (app.getSwarmServiceId()) {
      await this.orchestrator.startService(
        app.getSwarmServiceId()!,
        app.getReplicas(),
      );
    } else {
      this.logger.log(`Creating Swarm service for app ${command.appId}`);
      const serviceId = await this.orchestrator.createService(
        buildServiceSpec(app),
      );
      app.setSwarmServiceId(serviceId);
    }

    app.start();
    await this.appRepository.save(app);
    this.eventBus.publishAll(app.pullDomainEvents());
  }
}
