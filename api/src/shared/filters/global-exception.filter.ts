import { ArgumentsHost, Catch, ExceptionFilter, Logger } from '@nestjs/common';
import { I18nService } from 'nestjs-i18n';
import { Response } from 'express';

import { ApiResponse } from '@/shared/models/api-response.model';
import { ExceptionHandlerFactory } from '@/shared/exception-handler/common/factories/exception-handler.factory';

@Catch()
export class GlobalExceptionFilter implements ExceptionFilter {
  private readonly logger = new Logger(GlobalExceptionFilter.name);

  constructor(
    private readonly exceptionHandlerFactory: ExceptionHandlerFactory,
    private readonly i18n: I18nService,
  ) {}

  catch(exception: unknown, host: ArgumentsHost): Response {
    this.logger.error(exception);

    const errorHandler = this.exceptionHandlerFactory.create(exception);
    const result = errorHandler.handle(exception);
    const response = host.switchToHttp().getResponse<Response>();

    return response
      .status(result.statusCode)
      .json(ApiResponse.error(this.i18n.t(result.message), result.content));
  }
}
