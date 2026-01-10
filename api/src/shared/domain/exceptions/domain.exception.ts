export abstract class DomainException extends Error {
  abstract code: string;
  readonly metadata?: Record<string, unknown>;
  readonly localizationKey?: string;

  constructor(
    message: string,
    metadata?: Record<string, unknown>,
    localizationKey?: string,
  ) {
    super(message);
    this.name = this.constructor.name;
    this.metadata = metadata;
    this.localizationKey = localizationKey;

    Object.setPrototypeOf(this, DomainException.prototype);

    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, this.constructor);
    }
  }
}
