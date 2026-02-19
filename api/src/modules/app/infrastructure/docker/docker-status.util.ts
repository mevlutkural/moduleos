import { AppStatusEnum } from '../../domain/value-objects/app-status.value-object';

interface TaskLike {
  Status?: { State?: string };
}

export function determineAppStatus(
  tasks: TaskLike[],
  desiredReplicas: number,
): AppStatusEnum {
  const runningTasks = tasks.filter(
    (t) => t.Status?.State === 'running',
  ).length;

  if (runningTasks === desiredReplicas && desiredReplicas > 0) {
    return AppStatusEnum.RUNNING;
  }

  if (runningTasks > 0) {
    return AppStatusEnum.DEPLOYING;
  }

  const failedTasks = tasks.filter(
    (t) => t.Status?.State === 'failed' || t.Status?.State === 'rejected',
  );

  if (failedTasks.length > 0) {
    return AppStatusEnum.FAILED;
  }

  return AppStatusEnum.CREATED;
}
