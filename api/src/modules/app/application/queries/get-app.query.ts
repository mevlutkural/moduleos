import { IQuery } from '@nestjs/cqrs';

export class GetAppQuery implements IQuery {
  constructor(public readonly appId: string) {}
}
