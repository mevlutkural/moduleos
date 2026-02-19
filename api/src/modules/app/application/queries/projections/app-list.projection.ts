export class AppListProjection {
  constructor(
    public readonly id: string,
    public readonly name: string,
    public readonly status: string,
    public readonly replicas: number,
    public readonly createdAt: Date,
  ) {}
}
