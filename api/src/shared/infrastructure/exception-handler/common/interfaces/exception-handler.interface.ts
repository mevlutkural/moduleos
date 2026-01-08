import { ExceptionResult } from '../models/exception-result.model';

export interface ExceptionHandler {
  handle(exception: unknown): ExceptionResult;
}
