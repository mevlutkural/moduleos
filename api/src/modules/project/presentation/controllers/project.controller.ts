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
} from '@nestjs/common';
import { CommandBus, QueryBus } from '@nestjs/cqrs';
import { CreateProjectCommand } from '../../application/commands/create-project.command';
import { UpdateProjectCommand } from '../../application/commands/update-project.command';
import { DeleteProjectCommand } from '../../application/commands/delete-project.command';
import { GetProjectQuery } from '../../application/queries/get-project.query';
import { GetProjectsQuery } from '../../application/queries/get-projects.query';
import { ProjectListProjection } from '../../application/queries/projections/project-list.projection';
import { ProjectDetailProjection } from '../../application/queries/projections/project-detail.projection';
import { CreateProjectDto, UpdateProjectDto, ProjectResponseDto } from '../dto';

@Controller('projects')
export class ProjectController {
  constructor(
    private readonly commandBus: CommandBus,
    private readonly queryBus: QueryBus,
  ) {}

  @Post()
  @HttpCode(HttpStatus.CREATED)
  async create(@Body() dto: CreateProjectDto): Promise<ProjectResponseDto> {
    const command = new CreateProjectCommand(dto.name, dto.description);
    return this.commandBus.execute<CreateProjectCommand, ProjectResponseDto>(
      command,
    );
  }

  @Get()
  async findAll(): Promise<ProjectListProjection[]> {
    return this.queryBus.execute<GetProjectsQuery, ProjectListProjection[]>(
      new GetProjectsQuery(),
    );
  }

  @Get(':id')
  async findOne(
    @Param('id', ParseUUIDPipe) id: string,
  ): Promise<ProjectDetailProjection> {
    return this.queryBus.execute<GetProjectQuery, ProjectDetailProjection>(
      new GetProjectQuery(id),
    );
  }

  @Put(':id')
  async update(
    @Param('id', ParseUUIDPipe) id: string,
    @Body() dto: UpdateProjectDto,
  ): Promise<ProjectResponseDto> {
    const command = new UpdateProjectCommand(id, dto.name, dto.description);
    return this.commandBus.execute<UpdateProjectCommand, ProjectResponseDto>(
      command,
    );
  }

  @Delete(':id')
  @HttpCode(HttpStatus.NO_CONTENT)
  async remove(@Param('id', ParseUUIDPipe) id: string): Promise<void> {
    const command = new DeleteProjectCommand(id);
    await this.commandBus.execute<DeleteProjectCommand>(command);
  }
}
