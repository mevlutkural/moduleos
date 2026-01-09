import { Injectable } from '@nestjs/common';
import { Project } from '../../domain';
import { ProjectResponseDto } from '../dto';

@Injectable()
export class ProjectMapper {
  toResponse(project: Project): ProjectResponseDto {
    return {
      id: project.getId().getValue(),
      name: project.name.value,
      description: project.description.value,
      createdAt: project.createdAt,
      updatedAt: project.updatedAt,
    };
  }

  toResponseList(projects: Project[]): ProjectResponseDto[] {
    return projects.map((project) => this.toResponse(project));
  }
}
