import { ConflictException } from '@nestjs/common';
import { ErrorMessages } from '@/shared/constants/error-messages';

export class AppNameAlreadyExistsException extends ConflictException {
  constructor(name: string, projectId: string) {
    super(
      ErrorMessages.App.NameAlreadyExists ??
        `App with name '${name}' already exists in project '${projectId}'`,
    );
  }
}
