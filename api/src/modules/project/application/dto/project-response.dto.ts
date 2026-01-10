import { Expose } from 'class-transformer';

export class ProjectResponseDto {
  @Expose()
  id: string;

  @Expose()
  name: string;

  @Expose()
  description: string | null;

  @Expose()
  createdAt: Date;

  @Expose()
  updatedAt: Date;
}
