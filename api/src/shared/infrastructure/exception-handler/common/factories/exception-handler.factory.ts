import { HttpException, Injectable } from '@nestjs/common';

import { RequestValidationException } from '@/shared/presentation/exceptions/request-validation.exception';

import { ExceptionHandler } from '../interfaces/exception-handler.interface';

import { ValidationExceptionHandler } from '../handlers/validation.exception-handler';
import { HttpExceptionHandler } from '../handlers/http.exception-handler';
import { UnknownExceptionHandler } from '../handlers/unknown.exception-handler';

@Injectable()
export class ExceptionHandlerFactory {
  constructor(
    private readonly validationExceptionHandler: ValidationExceptionHandler,
    private readonly httpExceptionHandler: HttpExceptionHandler,
    private readonly unknownExceptionHandler: UnknownExceptionHandler,
  ) {}

  create(exception: unknown): ExceptionHandler {
    if (this.isValidationException(exception)) {
      return this.validationExceptionHandler;
    }

    if (this.isHttpException(exception)) {
      return this.httpExceptionHandler;
    }

    return this.unknownExceptionHandler;
  }

  private isValidationException(exception: unknown): boolean {
    return exception instanceof RequestValidationException;
  }

  private isHttpException(exception: unknown): boolean {
    return exception instanceof HttpException;
  }
}
