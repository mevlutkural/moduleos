import { Injectable } from '@nestjs/common';
import { ValidationError } from 'class-validator';
import { I18nService } from 'nestjs-i18n';

interface FieldError {
  field: string;
  message: string;
}

interface ValidationLimits {
  min: number | null;
  max: number | null;
  minLength: number | null;
  maxLength: number | null;
  values: string[] | number[] | null;
  enumValues: string[] | number[] | null;
  duplicates: string | null;
}

@Injectable()
export class ValidationErrorFormatter {
  private readonly CONSTRAINT_PRIORITY_MAP: Record<string, number> = {
    isNotEmpty: 1,
    isDefined: 1,

    isString: 2,
    isNumber: 2,
    isInt: 2,
    isBoolean: 2,
    isEnum: 2,
    isArray: 2,
    isObject: 2,

    isEmail: 3,
    isUuid: 3,
    isUrl: 3,
    isIp: 3,
    isPhoneNumber: 3,
    isDateString: 3,
  };

  constructor(private readonly i18n: I18nService) {}

  public format(errors: ValidationError[]): FieldError[] {
    return this.processErrors(errors);
  }

  private processErrors(
    errors: ValidationError[],
    parentPath: string = '',
  ): FieldError[] {
    const fieldErrorMap = new Map<string, FieldError>();

    for (const error of errors) {
      if (error.constraints?.whitelistValidation) {
        const forbiddenFieldError = this.handleWhitelistValidation(error);
        if (!fieldErrorMap.has(forbiddenFieldError.field)) {
          fieldErrorMap.set(forbiddenFieldError.field, forbiddenFieldError);
        }
        continue;
      }

      const currentPath = this.buildFieldPath(parentPath, error.property);

      if (error.constraints && !fieldErrorMap.has(currentPath)) {
        const formattedError = this.formatConstraintError(error, currentPath);
        fieldErrorMap.set(currentPath, formattedError);
      }

      if (error.children && error.children.length > 0) {
        const childErrors = this.processErrors(error.children, currentPath);

        childErrors.forEach((childError) => {
          if (!fieldErrorMap.has(childError.field)) {
            fieldErrorMap.set(childError.field, childError);
          }
        });
      }
    }

    return Array.from(fieldErrorMap.values());
  }

  private buildFieldPath(parentPath: string, property: string): string {
    if (!parentPath) {
      return property;
    }

    const isArrayIndex = !Number.isNaN(Number(property));

    return isArrayIndex
      ? `${parentPath}[${property}]`
      : `${parentPath}.${property}`;
  }

  private handleWhitelistValidation(error: ValidationError): FieldError {
    const message = this.i18n.t('validation.forbiddenField', {
      args: {
        field: error.property,
      },
    });

    return {
      field: error.property,
      message,
    };
  }

  private formatConstraintError(
    error: ValidationError,
    currentPath: string,
  ): FieldError {
    const constraints = Object.keys(error.constraints!);

    const constraintKey = constraints.reduce((prev, curr) => {
      const prevPriority = this.CONSTRAINT_PRIORITY_MAP[prev] ?? 4;
      const currPriority = this.CONSTRAINT_PRIORITY_MAP[curr] ?? 4;
      return currPriority < prevPriority ? curr : prev;
    }, constraints[0]);

    const constraintMessage = error.constraints![constraintKey];

    const contextField = error.contexts?.[constraintKey]?.field;
    const fieldNameForTranslation = contextField || 'fields.' + currentPath;

    const limits = this.extractLimits(error.contexts, error.target);

    const translatedFieldName = this.i18n.t(fieldNameForTranslation);

    const localizedMessage = this.i18n.t(constraintMessage, {
      args: {
        field: translatedFieldName,
        ...limits,
      },
    });

    return {
      field: currentPath,
      message: localizedMessage as string,
    };
  }

  private extractLimits(
    contexts?: Record<string, any>,
    target?: object,
  ): ValidationLimits | null {
    if (!contexts && !target) {
      return null;
    }

    return {
      min: contexts?.min?.value ?? null,
      max: contexts?.max?.value ?? null,
      minLength: contexts?.minLength?.value ?? null,
      maxLength: contexts?.maxLength?.value ?? null,
      values: contexts?.isIn?.value ?? null,
      enumValues: contexts?.isEnum?.value ?? null,
      duplicates: (target as any)?.['__duplicates'] ?? null,
    };
  }
}
