import { ICommand } from '@nestjs/cqrs';

export class StopAppCommand implements ICommand {
  constructor(public readonly appId: string) {}
}
