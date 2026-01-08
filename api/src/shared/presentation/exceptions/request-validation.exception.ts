import { BadRequestException } from '@nestjs/common';
import { ValidationError } from 'class-validator';

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
