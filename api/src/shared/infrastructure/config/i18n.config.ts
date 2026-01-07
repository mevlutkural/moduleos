import { registerAs } from '@nestjs/config';
import { join } from 'node:path';

export const i18nConfig = registerAs('i18n', () => ({
  fallbackLanguage: 'en',
  loaderOptions: {
    path: join(__dirname, '../../../lib/i18n'),
    watch: process.env.NODE_ENV === 'development',
  },
}));
