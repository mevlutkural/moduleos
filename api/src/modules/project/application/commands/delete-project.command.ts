import { ICommand } from '@nestjs/cqrs';

export class DeleteProjectCommand implements ICommand {
  constructor(public readonly id: string) {}
}
