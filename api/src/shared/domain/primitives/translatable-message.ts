export class TranslatableMessage {
  private constructor(
    private readonly key: string,
    private readonly args?: Record<string, unknown>,
  ) {}

  static fromKey(
    key: string,
    args?: Record<string, unknown>,
  ): TranslatableMessage {
    return new TranslatableMessage(key, args);
  }

  getKey(): string {
    return this.key;
  }

  getArgs(): Record<string, unknown> | undefined {
    return this.args;
  }

  static isTranslatableMessage(
    data: string | object,
  ): data is TranslatableMessage {
    if (typeof data === 'string') return false;
    if (data instanceof TranslatableMessage) return true;
    if ('key' in data && 'args' in data) return true;
    return false;
  }
}
