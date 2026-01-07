import { Type } from 'class-transformer';
import { IsInt, IsNotEmpty, Max, Min } from 'class-validator';
import { VALIDATION_MESSAGES } from '../../constants/validation-messages';
import { FIELDS } from '../../constants/fields';

export class PaginationQueryDto {
  @IsNotEmpty({
    message: VALIDATION_MESSAGES.REQUIRED,
    context: { field: FIELDS.GENERAL.PAGE },
  })
  @IsInt({
    message: VALIDATION_MESSAGES.INT,
    context: { field: FIELDS.GENERAL.PAGE },
  })
  @Min(1, {
    message: VALIDATION_MESSAGES.MIN,
    context: { field: FIELDS.GENERAL.PAGE, value: 1 },
  })
  @Type(() => Number)
  page: number;

  @IsNotEmpty({
    message: VALIDATION_MESSAGES.REQUIRED,
    context: { field: FIELDS.GENERAL.LIMIT },
  })
  @IsInt({
    message: VALIDATION_MESSAGES.INT,
    context: { field: FIELDS.GENERAL.LIMIT },
  })
  @Min(1, {
    message: VALIDATION_MESSAGES.MIN,
    context: { field: FIELDS.GENERAL.LIMIT, value: 1 },
  })
  @Max(1000, {
    message: VALIDATION_MESSAGES.MAX,
    context: { field: FIELDS.GENERAL.LIMIT, value: 1000 },
  })
  @Type(() => Number)
  limit: number;
}
