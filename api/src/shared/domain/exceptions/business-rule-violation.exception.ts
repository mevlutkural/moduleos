import { DomainException } from './domain.exception';

export class BusinessRuleViolationException extends DomainException {
  code = 'BUSINESS_RULE_VIOLATION';

  constructor(
    rule: string,
    details?: Record<string, unknown>,
    localizationKey?: string,
  ) {
    super(`Business rule violated: ${rule}`, details, localizationKey);
  }
}
