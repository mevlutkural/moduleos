import { AppError } from "./app.error";

export class UnknownError extends AppError {
  public readonly originalError: unknown;

  constructor(message: string, originalError?: unknown) {
    super(message);
    this.originalError = originalError;
    this.name = this.constructor.name;
    Object.setPrototypeOf(this, UnknownError.prototype);
  }
}
