import { App } from '../../domain';

export const CONTAINER_ORCHESTRATOR = Symbol('CONTAINER_ORCHESTRATOR');

export interface ServiceInfo {
  id: string;
  status: string;
  replicas: { running: number; desired: number };
}

export interface ContainerOrchestrator {
  createService(app: App, projectId: string): Promise<string>;
  updateService(app: App, projectId: string): Promise<void>;
  removeService(serviceId: string): Promise<void>;
  getServiceInfo(serviceId: string): Promise<ServiceInfo | null>;
}
