import { Module } from '@nestjs/common';
import { AppController } from './app.controller';
import { AppService } from './app.service';
import { ExceptionHandlerModule } from './shared/exception-handler/exception-handler.module';
import { APP_FILTER } from '@nestjs/core';
import { GlobalExceptionFilter } from './shared/filters/global-exception.filter';
import { TypeOrmModule } from '@nestjs/typeorm';
import { ConfigModule } from '@nestjs/config';
import { dbConfig, DbConfigService } from './shared/config/db.config';
import { ThrottlerModule } from '@nestjs/throttler';
import {
  throttlerConfig,
  ThrottlerConfigService,
} from './shared/config/throttler.config';
import { I18nModule } from 'nestjs-i18n';
import { i18nConfig, I18nConfigService } from './shared/config/i18n.config';

@Module({
  imports: [
    TypeOrmModule.forRootAsync({
      imports: [ConfigModule.forFeature(dbConfig)],
      useClass: DbConfigService,
    }),
    I18nModule.forRootAsync({
      imports: [ConfigModule.forFeature(i18nConfig)],
      useClass: I18nConfigService,
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
