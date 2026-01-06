import { Global, Module } from '@nestjs/common';

import { HttpExceptionHandler } from './common/handlers/http.exception-handler';
import { ValidationExceptionHandler } from './common/handlers/validation.exception-handler';
import { UnknownExceptionHandler } from './common/handlers/unknown.exception-handler';
import { ExceptionHandlerFactory } from './common/factories/exception-handler.factory';
import { ValidationErrorFormatter } from './validation-exception-handler/validation-error-formatter';

@Global()
@Module({
  providers: [
    HttpExceptionHandler,
    ValidationExceptionHandler,
    UnknownExceptionHandler,
    ValidationErrorFormatter,
    ExceptionHandlerFactory,
  ],
  exports: [ExceptionHandlerFactory],
})
export class ExceptionHandlerModule {}
