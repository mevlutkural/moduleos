import { v4 as uuidv4 } from 'uuid';

export abstract class Identifier<T = string> {
  protected readonly value: T;

  protected constructor(value: T) {
    this.value = value;
  }

  getValue(): T {
    return this.value;
  }

  equals(other: Identifier<T>): boolean {
    if (other === null || other === undefined) {
      return false;
    }

    if (!(other instanceof Identifier)) {
      return false;
    }

    return this.value === other.value;
  }

  toString(): string {
    return String(this.value);
  }

  protected static generateUuid(): string {
    return uuidv4();
  }
}

export abstract class UuidIdentifier extends Identifier<string> {
  protected constructor(value: string) {
    super(value);
    this.ensureValidUuid(value);
  }

  private ensureValidUuid(value: string): void {
    const uuidRegex =
      /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
    if (!uuidRegex.test(value)) {
      throw new Error(`Invalid UUID format: ${value}`);
    }
  }
}
