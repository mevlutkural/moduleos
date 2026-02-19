import { ValueObject, InvalidValueException } from '@/shared/domain';
import {
  APP_NAME_PATTERN,
  APP_NAME_MIN_LENGTH,
  APP_NAME_MAX_LENGTH,
  APP_NAME_VALIDATION_MESSAGE,
} from '../constants/app-name.constants';

interface AppNameProps {
  value: string;
}

export class AppName extends ValueObject<AppNameProps> {
  private constructor(props: AppNameProps) {
    super(props);
  }

  static create(value: string): AppName {
    const trimmedValue = value?.trim().toLowerCase();

    if (!trimmedValue || trimmedValue.length < APP_NAME_MIN_LENGTH) {
      throw new InvalidValueException('AppName', 'Name cannot be empty');
    }

    if (trimmedValue.length > APP_NAME_MAX_LENGTH) {
      throw new InvalidValueException(
        'AppName',
        `Name cannot exceed ${APP_NAME_MAX_LENGTH} characters`,
      );
    }

    if (!APP_NAME_PATTERN.test(trimmedValue)) {
      throw new InvalidValueException('AppName', APP_NAME_VALIDATION_MESSAGE);
    }

    return new AppName({ value: trimmedValue });
  }

  get value(): string {
    return this.props.value;
  }
}
