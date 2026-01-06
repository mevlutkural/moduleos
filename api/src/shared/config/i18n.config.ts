import { Inject } from '@nestjs/common';
import { ConfigType, registerAs } from '@nestjs/config';
import {
  AcceptLanguageResolver,
  I18nOptions,
  I18nOptionsFactory,
} from 'nestjs-i18n';
import { join } from 'node:path';

export const i18nConfig = registerAs('i18n', () => ({
  fallbackLanguage: process.env.I18N_FALLBACK_LANGUAGE ?? 'en',
  watch: process.env.NODE_ENV !== 'production',
}));

export type I18nConfig = ConfigType<typeof i18nConfig>;

export const InjectI18nConfig = () => Inject(i18nConfig.KEY);

export class I18nConfigService implements I18nOptionsFactory {
  constructor(
    @Inject(i18nConfig.KEY)
    private readonly config: I18nConfig,
  ) {}

  createI18nOptions(): I18nOptions {
    return {
      fallbackLanguage: this.config.fallbackLanguage,
      loaderOptions: {
        path: join(__dirname, '../../lib/i18n'),
        watch: this.config.watch,
      },
      resolvers: [AcceptLanguageResolver],
    };
  }
}
