import {
  Entity,
  PrimaryGeneratedColumn,
  Column,
  ManyToOne,
  JoinColumn,
} from 'typeorm';
import { AppOrmEntity } from './app.orm-entity';

@Entity('app_env_vars')
export class AppEnvVarOrmEntity {
  @PrimaryGeneratedColumn('uuid')
  id: string;

  @Column({ name: 'app_id', type: 'uuid' })
  appId: string;

  @ManyToOne(() => AppOrmEntity, (app) => app.envVars, { onDelete: 'CASCADE' })
  @JoinColumn({ name: 'app_id' })
  app: AppOrmEntity;

  @Column({ type: 'varchar', length: 255 })
  key: string;

  @Column({ type: 'text' })
  value: string;
}
