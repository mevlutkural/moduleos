import { EntityNotFoundException } from '@/shared/domain';
import { ErrorMessages } from '@/shared/constants/error-messages';

export class AppNotFoundException extends EntityNotFoundException {
  readonly code = 'APP_NOT_FOUND';

  constructor(id: string) {
    super('App', id, ErrorMessages.App.NotFound);
  }
}
