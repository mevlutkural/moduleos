import { CommandHandler, ICommandHandler, EventBus } from '@nestjs/cqrs';
import { Inject } from '@nestjs/common';
import {
  type AppRepository,
  APP_REPOSITORY,
  AppId,
  AppNotFoundException,
} from '../../domain';
import { UpdateAppConfigCommand } from './update-app-config.command';
import { AppResponseMapper } from '../mappers/app-response.mapper';
import { AppResponseDto } from '../dto/app-response.dto';
import { DockerSwarmService } from '../../infrastructure/docker/docker-swarm.service';

@CommandHandler(UpdateAppConfigCommand)
export class UpdateAppConfigHandler implements ICommandHandler<UpdateAppConfigCommand> {
  constructor(
    @Inject(APP_REPOSITORY)
    private readonly appRepository: AppRepository,
    private readonly mapper: AppResponseMapper,
    private readonly eventBus: EventBus,
    private readonly dockerSwarmService: DockerSwarmService,
  ) {}

  async execute(command: UpdateAppConfigCommand): Promise<AppResponseDto> {
    const appId = AppId.fromString(command.appId);
    const app = await this.appRepository.findById(appId);

    if (!app) {
      throw new AppNotFoundException(command.appId);
    }

    app.updateConfig({
      containerPort: command.containerPort,
      replicas: command.replicas,
      restartPolicy: command.restartPolicy,
      memoryLimit: command.memoryLimit,
      cpuLimit: command.cpuLimit,
    });

    if (command.envVars !== undefined) {
      app.updateEnvVars(command.envVars);
    }

    await this.appRepository.save(app);

    await this.dockerSwarmService.updateService(app);

    this.eventBus.publishAll(app.pullDomainEvents());

    return this.mapper.toResponse(app);
  }
}
