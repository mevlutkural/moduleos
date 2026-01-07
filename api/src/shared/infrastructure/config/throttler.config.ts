import { Inject } from '@nestjs/common';
import { ConfigType, registerAs } from '@nestjs/config';
import {
  ThrottlerModuleOptions,
  ThrottlerOptionsFactory,
} from '@nestjs/throttler';

export const throttlerConfig = registerAs('throttler', () => ({
  ttl: Number.parseInt(process.env.THROTTLE_TTL ?? '60000', 10),
  limit: Number.parseInt(process.env.THROTTLE_LIMIT ?? '60', 10),
}));

export type ThrottlerConfig = ConfigType<typeof throttlerConfig>;

export const InjectThrottlerConfig = () => Inject(throttlerConfig.KEY);
export class ThrottlerConfigService implements ThrottlerOptionsFactory {
  constructor(
    @Inject(throttlerConfig.KEY)
    private readonly config: ThrottlerConfig,
  ) {}

  createThrottlerOptions(): ThrottlerModuleOptions {
    return [
      {
        ttl: this.config.ttl,
        limit: this.config.limit,
      },
    ];
  }
}
