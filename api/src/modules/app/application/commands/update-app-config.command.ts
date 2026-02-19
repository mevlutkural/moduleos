import { ICommand } from '@nestjs/cqrs';
import { AppEnvVar } from '../../domain';

export class UpdateAppConfigCommand implements ICommand {
  constructor(
    public readonly appId: string,
    public readonly containerPort?: number,
    public readonly replicas?: number,
    public readonly restartPolicy?: string,
    public readonly memoryLimit?: string | null,
    public readonly cpuLimit?: string | null,
    public readonly envVars?: AppEnvVar[],
    public readonly image?: string,
  ) {}
}
