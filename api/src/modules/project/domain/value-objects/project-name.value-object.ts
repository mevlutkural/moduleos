import { ValueObject, InvalidValueException } from '@/shared/domain';

interface ProjectNameProps {
  value: string;
}

export class ProjectName extends ValueObject<ProjectNameProps> {
  private static readonly MIN_LENGTH = 1;
  private static readonly MAX_LENGTH = 255;

  private constructor(props: ProjectNameProps) {
    super(props);
  }

  static create(value: string): ProjectName {
    const trimmedValue = value?.trim();

    if (!trimmedValue || trimmedValue.length < ProjectName.MIN_LENGTH) {
      throw new InvalidValueException('ProjectName', 'Name cannot be empty');
    }

    if (trimmedValue.length > ProjectName.MAX_LENGTH) {
      throw new InvalidValueException(
        'ProjectName',
        `Name cannot exceed ${ProjectName.MAX_LENGTH} characters`,
      );
    }

    return new ProjectName({ value: trimmedValue });
  }

  get value(): string {
    return this.props.value;
  }
}
