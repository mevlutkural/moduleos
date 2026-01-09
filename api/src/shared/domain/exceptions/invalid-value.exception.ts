import { DomainException } from './domain.exception';

export class InvalidValueException extends DomainException {
  code = 'INVALID_VALUE';

  constructor(valueName: string, reason: string, localizationKey?: string) {
    super(
      `Invalid ${valueName}: ${reason}`,
      { valueName, reason },
      localizationKey,
    );
  }
}
