import { Expose } from 'class-transformer';

export class AppResponseDto {
  @Expose()
  id: string;

  @Expose()
  name: string;

  @Expose()
  projectId: string;

  @Expose()
  status: string;

  @Expose()
  containerPort: number;

  @Expose()
  replicas: number;

  @Expose()
  restartPolicy: string;

  @Expose()
  memoryLimit: string | null;

  @Expose()
  cpuLimit: string | null;

  @Expose()
  swarmServiceId: string | null;

  @Expose()
  image: string;

  @Expose()
  createdAt: Date;

  @Expose()
  updatedAt: Date;
}
