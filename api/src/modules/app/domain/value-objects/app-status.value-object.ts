export enum AppStatusEnum {
  CREATED = 'created',
  RUNNING = 'running',
  STOPPED = 'stopped',
  DEPLOYING = 'deploying',
  FAILED = 'failed',
  UPDATING = 'updating',
}

export class AppStatus {
  private constructor(private readonly status: AppStatusEnum) {}

  static create(status: AppStatusEnum): AppStatus {
    return new AppStatus(status);
  }

  static created(): AppStatus {
    return new AppStatus(AppStatusEnum.CREATED);
  }

  get value(): AppStatusEnum {
    return this.status;
  }

  equals(other: AppStatus): boolean {
    return this.status === other.status;
  }

  isRunning(): boolean {
    return this.status === AppStatusEnum.RUNNING;
  }

  isStopped(): boolean {
    return this.status === AppStatusEnum.STOPPED;
  }

  isFailed(): boolean {
    return this.status === AppStatusEnum.FAILED;
  }
}
