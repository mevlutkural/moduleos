import { AppError } from "./app.error";

export class ApiError extends AppError {
  public readonly statusCode: number;

  constructor(message: string, statusCode: number) {
    super(message);
    this.statusCode = statusCode;
    this.name = this.constructor.name;
    Object.setPrototypeOf(this, ApiError.prototype);
  }
}
