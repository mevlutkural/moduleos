import { ApiError } from "./api.error";

import type { FieldError } from "../models/field-error.model";

import httpStatus from "http-status";

export class ValidationError extends ApiError {
  public readonly errors: FieldError[];

  constructor(message: string, errors: FieldError[]) {
    super(message, httpStatus.BAD_REQUEST);
    this.errors = errors;
    this.name = this.constructor.name;
    Object.setPrototypeOf(this, ValidationError.prototype);
  }
}
