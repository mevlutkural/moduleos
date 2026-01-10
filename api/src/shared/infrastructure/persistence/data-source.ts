import { DataSource } from 'typeorm';
import { join } from 'node:path';
import { ProjectOrmEntity } from '@/modules/project/infrastructure/persistence/entities/project.orm-entity';

export const AppDataSource = new DataSource({
  type: 'better-sqlite3',
  database: process.env.DATABASE ?? 'moduleos.db',
  entities: [ProjectOrmEntity],
  migrations: [join(process.cwd(), 'src/database/migrations/*{.ts,.js}')],
  synchronize: false,
});
