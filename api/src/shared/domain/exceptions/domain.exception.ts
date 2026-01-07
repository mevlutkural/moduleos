export abstract class DomainException extends Error {
  abstract readonly code: string;

  readonly metadata?: Record<string, unknown>;

  constructor(message: string, metadata?: Record<string, unknown>) {
    super(message);
    this.name = this.constructor.name;
    this.metadata = metadata;

    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, this.constructor);
    }
  }
}

export class EntityNotFoundException extends DomainException {
  readonly code = 'ENTITY_NOT_FOUND';

  constructor(entityName: string, id: string) {
    super(`${entityName} with id '${id}' was not found`, {
      entityName,
      id,
    });
  }
}

export class BusinessRuleViolationException extends DomainException {
  readonly code = 'BUSINESS_RULE_VIOLATION';

  constructor(rule: string, details?: Record<string, unknown>) {
    super(`Business rule violated: ${rule}`, details);
  }
}

export class InvalidValueException extends DomainException {
  readonly code = 'INVALID_VALUE';

  constructor(valueName: string, reason: string) {
    super(`Invalid ${valueName}: ${reason}`, {
      valueName,
      reason,
    });
  }
}
