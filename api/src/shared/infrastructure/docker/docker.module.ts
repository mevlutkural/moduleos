import { Global, Module } from '@nestjs/common';
import { DockerClientProvider } from './docker-client.provider';

@Global()
@Module({
  providers: [DockerClientProvider],
  exports: [DockerClientProvider],
})
export class DockerModule {}
