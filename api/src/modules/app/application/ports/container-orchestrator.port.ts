export const CONTAINER_ORCHESTRATOR = Symbol('CONTAINER_ORCHESTRATOR');

export interface ServiceSpec {
  appId: string;
  projectId: string;
  name: string;
  image: string;
  containerPort: number;
  replicas: number;
  restartPolicy: string;
  memoryLimit: string | null;
  cpuLimit: string | null;
  envVars: Array<{ key: string; value: string }>;
}

export interface ServiceInfo {
  id: string;
  status: string;
  replicas: { running: number; desired: number };
}

export interface ContainerOrchestrator {
  createService(spec: ServiceSpec): Promise<string>;
  updateService(serviceId: string, spec: ServiceSpec): Promise<void>;
  removeService(serviceId: string): Promise<void>;
  getServiceInfo(serviceId: string): Promise<ServiceInfo | null>;
  stopService(serviceId: string): Promise<void>;
  startService(serviceId: string, replicas: number): Promise<void>;
}
