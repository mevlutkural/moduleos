import { Injectable } from '@nestjs/common';
import { App } from '../../domain';
import { AppResponseDto } from '../dto/app-response.dto';
import { DtoTransformer } from '@/shared/presentation';

@Injectable()
export class AppResponseMapper {
  toResponse(app: App): AppResponseDto {
    return DtoTransformer.toDto(AppResponseDto, app);
  }

  toResponseList(apps: App[]): AppResponseDto[] {
    return apps.map((app) => this.toResponse(app));
  }
}
