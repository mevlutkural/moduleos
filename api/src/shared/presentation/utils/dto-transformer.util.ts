import { ClassConstructor, plainToInstance } from 'class-transformer';

/**
 * Utility for transforming plain objects to DTO class instances.
 * Part of presentation layer as it handles DTO transformation.
 */
export class DtoTransformer {
  static toDto<T, V>(cls: ClassConstructor<T>, plain: V): T {
    return plainToInstance(cls, plain, {
      excludeExtraneousValues: true,
      exposeDefaultValues: true,
    });
  }
}
