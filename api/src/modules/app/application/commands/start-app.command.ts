import { ICommand } from '@nestjs/cqrs';

export class StartAppCommand implements ICommand {
  constructor(public readonly appId: string) {}
}
