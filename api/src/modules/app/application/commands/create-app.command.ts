import { ICommand } from '@nestjs/cqrs';

export class CreateAppCommand implements ICommand {
  constructor(
    public readonly name: string,
    public readonly projectId: string,
  ) {}
}
