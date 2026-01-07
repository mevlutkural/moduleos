/**
 * Standard API response wrapper for HTTP responses.
 * Used to provide consistent response format across all endpoints.
 */
export class ApiResponse<T> {
  private constructor(
    public message: string,
    public content: T | null,
    public timestamp: Date,
  ) {}

  static success<T>(message: string, data: T): ApiResponse<T> {
    return new ApiResponse<T>(message, data, new Date());
  }

  static error<T>(message: string, data: T | null = null): ApiResponse<T> {
    return new ApiResponse<T>(message, data, new Date());
  }
}
