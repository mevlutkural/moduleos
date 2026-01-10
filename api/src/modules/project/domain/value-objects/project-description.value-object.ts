import { ValueObject, InvalidValueException } from '@/shared/domain';

interface ProjectDescriptionProps {
  value: string | null;
}

export class ProjectDescription extends ValueObject<ProjectDescriptionProps> {
  private static readonly MAX_LENGTH = 2000;

  private constructor(props: ProjectDescriptionProps) {
    super(props);
  }

  static create(value: string | null | undefined): ProjectDescription {
    if (value === null || value === undefined) {
      return new ProjectDescription({ value: null });
    }

    const trimmedValue = value.trim();

    if (trimmedValue === '') {
      return new ProjectDescription({ value: null });
    }

    if (trimmedValue.length > ProjectDescription.MAX_LENGTH) {
      throw new InvalidValueException(
        'ProjectDescription',
        `Description cannot exceed ${ProjectDescription.MAX_LENGTH} characters`,
      );
    }

    return new ProjectDescription({ value: trimmedValue });
  }

  get value(): string | null {
    return this.props.value;
  }

  get isNull(): boolean {
    return this.props.value === null;
  }
}
