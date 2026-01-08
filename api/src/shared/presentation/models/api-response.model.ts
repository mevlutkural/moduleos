export class ApiResponse<T> {
  private constructor(
    public message: string,
    public content: T | null,
    public timestamp: Date,
  ) {}

  static success<T>(message: string, content: T): ApiResponse<T> {
    return new ApiResponse<T>(message, content, new Date());
  }

  static error<T>(message: string, content: T | null = null): ApiResponse<T> {
    return new ApiResponse<T>(message, content, new Date());
  }
}
