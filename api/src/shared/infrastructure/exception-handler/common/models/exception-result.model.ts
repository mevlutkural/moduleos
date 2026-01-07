interface ExceptionResultParams<T = unknown> {
  message: string;
  statusCode: number;
  content: T;
}

export class ExceptionResult<T = unknown> {
  private constructor(
    public readonly message: string,
    public readonly statusCode: number,
    public readonly content: T,
  ) {}

  static create<T>(params: ExceptionResultParams<T>): ExceptionResult<T> {
    return new ExceptionResult(
      params.message,
      params.statusCode,
      params.content,
    );
  }
}
