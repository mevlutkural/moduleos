import { BadRequestException } from '@nestjs/common';
import { ValidationError } from 'class-validator';

/**
 * Exception thrown when request validation fails.
 * Part of presentation layer as it's HTTP/NestJS specific.
 */
export class RequestValidationException extends BadRequestException {
  private readonly errors: ValidationError[];

  constructor(errors: ValidationError[]) {
    super(errors);
    this.errors = errors;
  }

  getErrors(): ValidationError[] {
    return this.errors;
  }
}
