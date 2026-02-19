import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import {
  type AppRepository,
  APP_REPOSITORY,
  AppId,
  AppNotFoundException,
} from '../../domain';
import { DeleteAppCommand } from './delete-app.command';
import {
  type ContainerOrchestrator,
  CONTAINER_ORCHESTRATOR,
} from '../ports/container-orchestrator.port';

@CommandHandler(DeleteAppCommand)
export class DeleteAppHandler implements ICommandHandler<DeleteAppCommand> {
  constructor(
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly eventBus: EventBus,
    @Inject(CONTAINER_ORCHESTRATOR)
    private readonly orchestrator: ContainerOrchestrator,
  ) {}

  async execute(command: DeleteAppCommand): Promise<void> {
    const appId = AppId.fromString(command.appId);
    const app = await this.appRepository.findById(appId);

    if (!app) {
      throw new AppNotFoundException(command.appId);
    }

    if (app.getSwarmServiceId()) {
      await this.orchestrator.removeService(app.getSwarmServiceId()!);
    }

    app.delete();
    this.eventBus.publishAll(app.pullDomainEvents());

    await this.appRepository.delete(appId);
  }
}
