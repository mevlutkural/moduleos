import { Module } from '@nestjs/common';
import { TypeOrmModule } from '@nestjs/typeorm';
import { CqrsModule } from '@nestjs/cqrs';
import { ScheduleModule } from '@nestjs/schedule';
import { AppController } from './presentation/controllers/app.controller';
import { AppOrmEntity } from './infrastructure/persistence/entities/app.orm-entity';
import { AppEnvVarOrmEntity } from './infrastructure/persistence/entities/app-env-var.orm-entity';
import { TypeOrmAppRepository } from './infrastructure/persistence/repositories/typeorm-app.repository';
import { TypeOrmAppQueryRepository } from './infrastructure/persistence/repositories/typeorm-app-query.repository';
import { APP_REPOSITORY } from './domain/repositories/app.repository';
import { APP_QUERY_REPOSITORY } from './application/queries/repositories/app-query.repository';
import { CreateAppHandler } from './application/commands/create-app.handler';
import { UpdateAppConfigHandler } from './application/commands/update-app-config.handler';
import { DeleteAppHandler } from './application/commands/delete-app.handler';
import { GetAppHandler } from './application/queries/get-app.handler';
import { GetAppsHandler } from './application/queries/get-apps.handler';
import { AppResponseMapper } from './application/mappers/app-response.mapper';
import { DockerSwarmService } from './infrastructure/docker/docker-swarm.service';
import { DockerEventListenerService } from './infrastructure/docker/docker-event-listener.service';
import { DockerReconciliationService } from './infrastructure/docker/docker-reconciliation.service';

const CommandHandlers = [
  CreateAppHandler,
  UpdateAppConfigHandler,
  DeleteAppHandler,
];

const QueryHandlers = [GetAppHandler, GetAppsHandler];

const DockerServices = [
  DockerSwarmService,
  DockerEventListenerService,
  DockerReconciliationService,
];

@Module({
  imports: [
    TypeOrmModule.forFeature([AppOrmEntity, AppEnvVarOrmEntity]),
    CqrsModule,
    ScheduleModule.forRoot(),
  ],
  controllers: [AppController],
  providers: [
    ...CommandHandlers,
    ...QueryHandlers,
    ...DockerServices,
    AppResponseMapper,
    {
      provide: APP_REPOSITORY,
      useClass: TypeOrmAppRepository,
    },
    {
      provide: APP_QUERY_REPOSITORY,
      useClass: TypeOrmAppQueryRepository,
    },
  ],
  exports: [APP_REPOSITORY, DockerSwarmService],
})
export class AppFeatureModule {}
