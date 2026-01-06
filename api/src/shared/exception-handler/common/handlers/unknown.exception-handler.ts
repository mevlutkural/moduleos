import { HttpStatus, Injectable } from '@nestjs/common';
import { ExceptionHandler } from '../interfaces/exception-handler.interface';
import { ExceptionResult } from '../models/exception-result.model';

@Injectable()
export class UnknownExceptionHandler implements ExceptionHandler {
  constructor() {}

  handle(): ExceptionResult {
    return ExceptionResult.create({
      message: 'errorMessages.general.internalServerError',
      statusCode: HttpStatus.INTERNAL_SERVER_ERROR,
      content: null,
    });
  }
}
