import { EntityNotFoundException } from '@/shared/domain';
import { ErrorMessages } from '@/shared/constants/error-messages';

export class ProjectNotFoundException extends EntityNotFoundException {
  readonly code = 'PROJECT_NOT_FOUND';

  constructor(id: string) {
    super('Project', id, ErrorMessages.Project.NotFound);
  }
}
