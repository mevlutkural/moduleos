import { Injectable } from '@nestjs/common';
import { ExceptionHandler } from '../interfaces/exception-handler.interface';
import { ExceptionResult } from '../models/exception-result.model';
import { ValidationErrorFormatter } from '../../validation-exception-handler/validation-error-formatter';
import { RequestValidationException } from '@/shared/exceptions/request-validation.exception';

@Injectable()
export class ValidationExceptionHandler implements ExceptionHandler {
  constructor(
    private readonly validationErrorFormatter: ValidationErrorFormatter,
  ) {}

  handle(exception: RequestValidationException): ExceptionResult {
    const formattedErrors = this.validationErrorFormatter.format(
      exception.getErrors(),
    );

    return ExceptionResult.create({
      message: 'errorMessages.common.validationError',
      statusCode: exception.getStatus(),
      content: formattedErrors,
    });
  }
}
