import {
  Body,
  Controller,
  Delete,
  Get,
  HttpCode,
  HttpStatus,
  Param,
  ParseUUIDPipe,
  Post,
  Put,
  Query,
} from '@nestjs/common';
import { CommandBus, QueryBus } from '@nestjs/cqrs';
import { CreateAppCommand } from '../../application/commands/create-app.command';
import { UpdateAppConfigCommand } from '../../application/commands/update-app-config.command';
import { DeleteAppCommand } from '../../application/commands/delete-app.command';
import { GetAppQuery } from '../../application/queries/get-app.query';
import { GetAppsQuery } from '../../application/queries/get-apps.query';
import { AppListProjection } from '../../application/queries/projections/app-list.projection';
import { AppDetailProjection } from '../../application/queries/projections/app-detail.projection';
import { CreateAppDto, UpdateAppConfigDto } from '../dto';
import { AppResponseDto } from '../../application/dto/app-response.dto';
import { BaseQueryDto } from '@/shared/presentation/dto/base-query.dto';
import { PaginatedResult } from '@/shared/application/query';
import { QueryParamsMapper } from '@/shared/presentation/mappers/query-params.mapper';
import { ApiResponse } from '@/shared/presentation';
import { I18n, I18nContext } from 'nestjs-i18n';
import { Messages } from '@/shared/constants/messages';

@Controller('projects/:projectId/apps')
export class AppController {
  constructor(
    private readonly commandBus: CommandBus,
    private readonly queryBus: QueryBus,
  ) {}

  @Post()
  @HttpCode(HttpStatus.CREATED)
  async create(
    @Param('projectId', ParseUUIDPipe) projectId: string,
    @Body() dto: CreateAppDto,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<AppResponseDto>> {
    const command = new CreateAppCommand(dto.name, projectId);
    const result = await this.commandBus.execute<
      CreateAppCommand,
      AppResponseDto
    >(command);

    return ApiResponse.success(i18n.translate(Messages.App.Created), result);
  }

  @Get()
  async findAll(
    @Param('projectId', ParseUUIDPipe) projectId: string,
    @Query() queryDto: BaseQueryDto,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<PaginatedResult<AppListProjection>>> {
    const result = await this.queryBus.execute<
      GetAppsQuery,
      PaginatedResult<AppListProjection>
    >(new GetAppsQuery(projectId, QueryParamsMapper.fromDto(queryDto)));

    return ApiResponse.success(i18n.translate(Messages.App.Listed), result);
  }

  @Get(':appId')
  async findOne(
    @Param('appId', ParseUUIDPipe) appId: string,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<AppDetailProjection>> {
    const result = await this.queryBus.execute<
      GetAppQuery,
      AppDetailProjection
    >(new GetAppQuery(appId));

    return ApiResponse.success(i18n.translate(Messages.App.Retrieved), result);
  }

  @Put(':appId')
  async update(
    @Param('appId', ParseUUIDPipe) appId: string,
    @Body() dto: UpdateAppConfigDto,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<AppResponseDto>> {
    const command = new UpdateAppConfigCommand(
      appId,
      dto.containerPort,
      dto.replicas,
      dto.restartPolicy,
      dto.memoryLimit,
      dto.cpuLimit,
      dto.envVars,
    );
    const result = await this.commandBus.execute<
      UpdateAppConfigCommand,
      AppResponseDto
    >(command);

    return ApiResponse.success(i18n.translate(Messages.App.Updated), result);
  }

  @Delete(':appId')
  @HttpCode(HttpStatus.NO_CONTENT)
  async remove(@Param('appId', ParseUUIDPipe) appId: string): Promise<void> {
    const command = new DeleteAppCommand(appId);
    await this.commandBus.execute<DeleteAppCommand>(command);
  }
}
