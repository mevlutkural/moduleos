import {
  Entity,
  PrimaryColumn,
  Column,
  CreateDateColumn,
  UpdateDateColumn,
  OneToMany,
  Unique,
} from 'typeorm';
import { AppEnvVarOrmEntity } from './app-env-var.orm-entity';

@Entity('apps')
@Unique(['projectId', 'name'])
export class AppOrmEntity {
  @PrimaryColumn('uuid')
  id: string;

  @Column({ type: 'varchar', length: 100 })
  name: string;

  @Column({ name: 'project_id', type: 'uuid' })
  projectId: string;

  @Column({ type: 'varchar', length: 20, default: 'created' })
  status: string;

  @Column({ name: 'container_port', type: 'integer', default: 80 })
  containerPort: number;

  @Column({ type: 'integer', default: 1 })
  replicas: number;

  @Column({
    name: 'restart_policy',
    type: 'varchar',
    length: 50,
    default: 'on-failure',
  })
  restartPolicy: string;

  @Column({ name: 'memory_limit', type: 'varchar', length: 50, nullable: true })
  memoryLimit: string | null;

  @Column({ name: 'cpu_limit', type: 'varchar', length: 50, nullable: true })
  cpuLimit: string | null;

  @Column({
    name: 'swarm_service_id',
    type: 'varchar',
    length: 255,
    nullable: true,
  })
  swarmServiceId: string | null;

  @Column({
    type: 'varchar',
    length: 255,
    default: 'nginx:alpine',
  })
  image: string;

  @OneToMany(() => AppEnvVarOrmEntity, (envVar) => envVar.app, {
    cascade: true,
    eager: true,
  })
  envVars: AppEnvVarOrmEntity[];

  @CreateDateColumn({ name: 'created_at' })
  createdAt: Date;

  @UpdateDateColumn({ name: 'updated_at' })
  updatedAt: Date;
}
