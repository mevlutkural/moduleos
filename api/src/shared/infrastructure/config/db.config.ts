import { ConfigType, registerAs } from '@nestjs/config';
import { join } from 'node:path';

export const dbConfig = registerAs('db', () => ({
  type: 'better-sqlite3' as const,
  database: process.env.DATABASE ?? 'moduleos.db',
  autoLoadEntities: true,
  entities: [
    join(__dirname, '../../../modules/**/*.orm-entity{.ts,.js}'),
    join(__dirname, '../../../database/example/*.orm-entity{.ts,.js}'),
  ],
  migrations: [join(__dirname, '../../../database/migrations/*{.ts,.js}')],
  synchronize: process.env.NODE_ENV === 'development',
  migrationsRun: process.env.NODE_ENV === 'production',
  logging: process.env.DB_LOGGING === 'true',
}));

export type DbConfig = ConfigType<typeof dbConfig>;
