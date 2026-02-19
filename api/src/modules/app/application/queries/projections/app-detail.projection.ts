export class AppDetailProjection {
  constructor(
    public readonly id: string,
    public readonly name: string,
    public readonly projectId: string,
    public readonly status: string,
    public readonly containerPort: number,
    public readonly replicas: number,
    public readonly restartPolicy: string,
    public readonly memoryLimit: string | null,
    public readonly cpuLimit: string | null,
    public readonly swarmServiceId: string | null,
    public readonly image: string,
    public readonly envVars: { key: string; value: string }[],
    public readonly createdAt: Date,
    public readonly updatedAt: Date,
  ) {}
}
