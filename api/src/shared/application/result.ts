export class Result<T, E = Error> {
  private constructor(
    private readonly _isSuccess: boolean,
    private readonly _value?: T,
    private readonly _error?: E,
  ) {}

  get isSuccess(): boolean {
    return this._isSuccess;
  }

  get isFailure(): boolean {
    return !this._isSuccess;
  }

  get value(): T {
    if (!this._isSuccess) {
      throw new Error('Cannot get value of a failed result');
    }
    return this._value as T;
  }

  get error(): E {
    if (this._isSuccess) {
      throw new Error('Cannot get error of a successful result');
    }
    return this._error as E;
  }

  static ok<T>(value: T): Result<T, never> {
    return new Result<T, never>(true, value);
  }

  static fail<E = Error>(error: E): Result<never, E> {
    return new Result<never, E>(false, undefined, error);
  }

  map<U>(fn: (value: T) => U): Result<U, E> {
    if (this._isSuccess) {
      return Result.ok(fn(this._value as T));
    }
    return Result.fail(this._error as E);
  }

  unwrap(): T {
    if (this._isSuccess) {
      return this._value as T;
    }
    if (this._error instanceof Error) {
      throw this._error;
    }
    throw new Error(String(this._error));
  }
}
