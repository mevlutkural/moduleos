import { Inject } from '@nestjs/common';
import { ConfigType, registerAs } from '@nestjs/config';
import { TypeOrmModuleOptions, TypeOrmOptionsFactory } from '@nestjs/typeorm';
import { join } from 'node:path';

export const dbConfig = registerAs('database', () => ({
  database: process.env.DB_DATABASE ?? 'moduleos.db',
  synchronize: process.env.NODE_ENV !== 'production',
  logging: process.env.DB_LOGGING === 'true',
}));

export type DbConfig = ConfigType<typeof dbConfig>;
export const InjectDbConfig = () => Inject(dbConfig.KEY);

export class DbConfigService implements TypeOrmOptionsFactory {
  constructor(
    @Inject(dbConfig.KEY)
    private readonly config: DbConfig,
  ) {}

  createTypeOrmOptions(): TypeOrmModuleOptions {
    return {
      type: 'better-sqlite3',
      database: this.config.database,
      autoLoadEntities: true,
      entities: [
        join(__dirname, '../../modules/**/*.entity{.ts,.js}'),
        join(__dirname, '../../modules/**/*.orm-entity{.ts,.js}'),
      ],
      synchronize: this.config.synchronize,
      logging: this.config.logging,
    };
  }
}
