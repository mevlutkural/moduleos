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
import { CreateProjectCommand } from '../../application/commands/create-project.command';
import { UpdateProjectCommand } from '../../application/commands/update-project.command';
import { DeleteProjectCommand } from '../../application/commands/delete-project.command';
import { GetProjectQuery } from '../../application/queries/get-project.query';
import { GetProjectsQuery } from '../../application/queries/get-projects.query';
import { ProjectListProjection } from '../../application/queries/projections/project-list.projection';
import { ProjectDetailProjection } from '../../application/queries/projections/project-detail.projection';
import { CreateProjectDto, UpdateProjectDto } from '../dto';
import { ProjectResponseDto } from '../../application/dto/project-response.dto';
import { BaseQueryDto } from '@/shared/presentation/dto/base-query.dto';
import { PaginatedResult } from '@/shared/application/query';
import { QueryParamsMapper } from '@/shared/presentation/mappers/query-params.mapper';
import { ApiResponse } from '@/shared/presentation';
import { I18n, I18nContext } from 'nestjs-i18n';
import { Messages } from '@/shared/constants/messages';

@Controller('projects')
export class ProjectController {
  constructor(
    private readonly commandBus: CommandBus,
    private readonly queryBus: QueryBus,
  ) {}

  @Post()
  @HttpCode(HttpStatus.CREATED)
  async create(
    @Body() dto: CreateProjectDto,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<ProjectResponseDto>> {
    const command = new CreateProjectCommand(dto.name, dto.description);
    const result = await this.commandBus.execute<
      CreateProjectCommand,
      ProjectResponseDto
    >(command);

    return ApiResponse.success(
      i18n.translate(Messages.Project.Created),
      result,
    );
  }

  @Get()
  async findAll(
    @Query() queryDto: BaseQueryDto,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<PaginatedResult<ProjectListProjection>>> {
    const result = await this.queryBus.execute<
      GetProjectsQuery,
      PaginatedResult<ProjectListProjection>
    >(new GetProjectsQuery(QueryParamsMapper.fromDto(queryDto)));

    return ApiResponse.success(i18n.translate(Messages.Project.Listed), result);
  }

  @Get(':id')
  async findOne(
    @Param('id', ParseUUIDPipe) id: string,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<ProjectDetailProjection>> {
    const result = await this.queryBus.execute<
      GetProjectQuery,
      ProjectDetailProjection
    >(new GetProjectQuery(id));

    return ApiResponse.success(
      i18n.translate(Messages.Project.Retrieved),
      result,
    );
  }

  @Put(':id')
  async update(
    @Param('id', ParseUUIDPipe) id: string,
    @Body() dto: UpdateProjectDto,
    @I18n() i18n: I18nContext,
  ): Promise<ApiResponse<ProjectResponseDto>> {
    const command = new UpdateProjectCommand(id, dto.name, dto.description);
    const result = await this.commandBus.execute<
      UpdateProjectCommand,
      ProjectResponseDto
    >(command);

    return ApiResponse.success(
      i18n.translate(Messages.Project.Updated),
      result,
    );
  }

  @Delete(':id')
  @HttpCode(HttpStatus.NO_CONTENT)
  async remove(@Param('id', ParseUUIDPipe) id: string): Promise<void> {
    const command = new DeleteProjectCommand(id);
    await this.commandBus.execute<DeleteProjectCommand>(command);
  }
}
