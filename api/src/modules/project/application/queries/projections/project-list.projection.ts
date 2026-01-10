export class ProjectListProjection {
  constructor(
    public readonly id: string,
    public readonly name: string,
    public readonly createdAt: Date,
  ) {}
}
