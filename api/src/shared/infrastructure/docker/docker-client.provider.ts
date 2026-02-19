import Docker from 'dockerode';

export const DOCKER_CLIENT = Symbol('DOCKER_CLIENT');

export const DockerClientProvider = {
  provide: DOCKER_CLIENT,
  useFactory: (): Docker => {
    return new Docker({ socketPath: '/var/run/docker.sock' });
  },
};
