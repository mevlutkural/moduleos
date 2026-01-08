import { DataSource } from 'typeorm';
import { runSeeders } from 'typeorm-extension';
import { join } from 'node:path';
import { MainSeeder } from './seeds/main.seeder';

const options = {
  type: 'better-sqlite3' as const,
  database: process.env.DATABASE ?? 'moduleos.db',
  entities: [
    join(__dirname, '../modules/**/persistence/*.orm-entity{.ts,.js}'),
  ],
};

const dataSource = new DataSource(options);

async function seed() {
  await dataSource.initialize();
  await runSeeders(dataSource, {
    seeds: [MainSeeder],
    factories: [],
  });
  await dataSource.destroy();
}

seed().catch(console.error);
