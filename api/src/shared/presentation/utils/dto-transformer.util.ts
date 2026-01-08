import { ClassConstructor, plainToInstance } from 'class-transformer';

export class DtoTransformer {
  static toDto<T, V>(cls: ClassConstructor<T>, plain: V): T {
    return plainToInstance(cls, plain, {
      excludeExtraneousValues: true,
      exposeDefaultValues: true,
    });
  }
}
