import { UuidIdentifier } from '@/shared/domain';

export class AppId extends UuidIdentifier {
  private constructor(value: string) {
    super(value);
  }

  static create(): AppId {
    return new AppId(this.generateUuid());
  }

  static fromString(value: string): AppId {
    return new AppId(value);
  }
}
