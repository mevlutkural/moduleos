import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import { App, type AppRepository, APP_REPOSITORY } from '../../domain';
import { CreateAppCommand } from './create-app.command';
import { AppResponseMapper } from '../mappers/app-response.mapper';
import { AppResponseDto } from '../dto/app-response.dto';
import { DockerSwarmService } from '../../infrastructure/docker/docker-swarm.service';

@CommandHandler(CreateAppCommand)
export class CreateAppHandler implements ICommandHandler<CreateAppCommand> {
  constructor(
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly mapper: AppResponseMapper,
    private readonly eventBus: EventBus,
    private readonly dockerSwarmService: DockerSwarmService,
  ) {}

  async execute(command: CreateAppCommand): Promise<AppResponseDto> {
    const app = App.create(command.name, command.projectId);

    await this.appRepository.save(app);

    const swarmServiceId = await this.dockerSwarmService.createService(
      app,
      command.projectId,
    );

    app.setSwarmServiceId(swarmServiceId);
    await this.appRepository.save(app);

    this.eventBus.publishAll(app.pullDomainEvents());

    return this.mapper.toResponse(app);
  }
}
