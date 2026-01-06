export class ApiResponse<T> {
  private constructor(
    public message: string,
    public content: T | null,
    public timestamp: Date,
  ) {}

  static success<T>(message: string, data: T) {
    return new ApiResponse<T>(message, data, new Date());
  }

  static error<T>(message: string, data: T | null = null) {
    return new ApiResponse<T>(message, data, new Date());
  }
}
