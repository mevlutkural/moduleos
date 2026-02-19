import { App } from '../../domain';
import { ServiceSpec } from '../ports/container-orchestrator.port';

export function buildServiceSpec(app: App): ServiceSpec {
  return {
    appId: app.getId().getValue(),
    projectId: app.getProjectId(),
    name: app.getName().value,
    image: app.getImage(),
    containerPort: app.getContainerPort(),
    replicas: app.getReplicas(),
    restartPolicy: app.getRestartPolicy(),
    memoryLimit: app.getMemoryLimit(),
    cpuLimit: app.getCpuLimit(),
    envVars: app.getEnvVars(),
  };
}
