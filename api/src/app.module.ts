import { Module } from '@nestjs/common';
import { AppController } from './app.controller';
import { AppService } from './app.service';
import { ExceptionHandlerModule } from './shared/exception-handler/exception-handler.module';
import { APP_FILTER } from '@nestjs/core';
import { GlobalExceptionFilter } from './shared/filters/global-exception.filter';
import { TypeOrmModule } from '@nestjs/typeorm';
import { ConfigModule } from '@nestjs/config';
import { dbConfig } from './shared/config/db.config';
import { ThrottlerModule } from '@nestjs/throttler';
import {
  throttlerConfig,
  ThrottlerConfigService,
} from './shared/config/throttler.config';
import { AcceptLanguageResolver, I18nModule } from 'nestjs-i18n';
import { i18nConfig } from './shared/config/i18n.config';

@Module({
  imports: [
    TypeOrmModule.forRootAsync(dbConfig.asProvider()),
    I18nModule.forRootAsync({
      ...i18nConfig.asProvider(),
      resolvers: [AcceptLanguageResolver],
    }),
    ExceptionHandlerModule,
    ThrottlerModule.forRootAsync({
      imports: [ConfigModule.forFeature(throttlerConfig)],
      useClass: ThrottlerConfigService,
    }),
  ],
  controllers: [AppController],
  providers: [
    AppService,
    {
      provide: APP_FILTER,
      useClass: GlobalExceptionFilter,
    },
  ],
})
export class AppModule {}
