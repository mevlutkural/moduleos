import { Injectable } from '@nestjs/common';
import { Project } from '../../domain';
import { ProjectResponseDto } from '../dto/project-response.dto';
import { DtoTransformer } from '@/shared/presentation';

@Injectable()
export class ProjectResponseMapper {
  toResponse(project: Project): ProjectResponseDto {
    return DtoTransformer.toDto(ProjectResponseDto, project);
  }

  toResponseList(projects: Project[]): ProjectResponseDto[] {
    return projects.map((project) => this.toResponse(project));
  }
}
