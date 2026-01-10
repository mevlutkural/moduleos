import { UuidIdentifier } from '@/shared/domain';

export class ProjectId extends UuidIdentifier {
  private constructor(value: string) {
    super(value);
  }

  static create(): ProjectId {
    return new ProjectId(this.generateUuid());
  }

  static fromString(value: string): ProjectId {
    return new ProjectId(value);
  }
}
