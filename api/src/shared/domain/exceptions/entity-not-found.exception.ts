import { DomainException } from './domain.exception';

export class EntityNotFoundException extends DomainException {
  code = 'ENTITY_NOT_FOUND';

  constructor(entityName: string, id: string, localizationKey?: string) {
    super(
      `${entityName} with id '${id}' was not found`,
      { entityName, id },
      localizationKey,
    );
  }
}
