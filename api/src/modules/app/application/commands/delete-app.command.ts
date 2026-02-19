import { ICommand } from '@nestjs/cqrs';

export class DeleteAppCommand implements ICommand {
  constructor(public readonly appId: string) {}
}
