import { HttpException, Injectable } from '@nestjs/common';
import { ExceptionHandler } from '../interfaces/exception-handler.interface';
import { ExceptionResult } from '../models/exception-result.model';

@Injectable()
export class HttpExceptionHandler implements ExceptionHandler {
  handle(exception: HttpException): ExceptionResult {
    return ExceptionResult.create({
      message: exception.message,
      statusCode: exception.getStatus(),
      content: null,
    });
  }
}
