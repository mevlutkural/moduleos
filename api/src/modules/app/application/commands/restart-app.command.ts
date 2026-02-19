import { ICommand } from '@nestjs/cqrs';

export class RestartAppCommand implements ICommand {
  constructor(public readonly appId: string) {}
}
