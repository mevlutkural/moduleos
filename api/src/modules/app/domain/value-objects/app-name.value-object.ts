import { ValueObject, InvalidValueException } from '@/shared/domain';

interface AppNameProps {
  value: string;
}

export class AppName extends ValueObject<AppNameProps> {
  private static readonly MIN_LENGTH = 1;
  private static readonly MAX_LENGTH = 100;
  private static readonly VALID_PATTERN =
    /^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$/;

  private constructor(props: AppNameProps) {
    super(props);
  }

  static create(value: string): AppName {
    const trimmedValue = value?.trim().toLowerCase();

    if (!trimmedValue || trimmedValue.length < AppName.MIN_LENGTH) {
      throw new InvalidValueException('AppName', 'Name cannot be empty');
    }

    if (trimmedValue.length > AppName.MAX_LENGTH) {
      throw new InvalidValueException(
        'AppName',
        `Name cannot exceed ${AppName.MAX_LENGTH} characters`,
      );
    }

    if (!AppName.VALID_PATTERN.test(trimmedValue)) {
      throw new InvalidValueException(
        'AppName',
        'Name must contain only lowercase letters, numbers, and dashes. Cannot start or end with a dash.',
      );
    }

    return new AppName({ value: trimmedValue });
  }

  get value(): string {
    return this.props.value;
  }
}
