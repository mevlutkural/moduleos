import { ICommand } from '@nestjs/cqrs';

export class CreateProjectCommand implements ICommand {
  constructor(
    public readonly name: string,
    public readonly description?: string,
  ) {}
}
