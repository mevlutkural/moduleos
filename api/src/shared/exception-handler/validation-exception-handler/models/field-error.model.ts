export class FieldError {
  constructor(
    public readonly field: string,
    public readonly message: string,
  ) {}
}
