import { DataSource } from 'typeorm';
import { join } from 'node:path';

export const AppDataSource = new DataSource({
  type: 'better-sqlite3',
  database: process.env.DATABASE ?? 'moduleos.db',
  entities: [],
  migrations: [join(process.cwd(), 'src/database/migrations/*{.ts,.js}')],
  synchronize: false,
});
