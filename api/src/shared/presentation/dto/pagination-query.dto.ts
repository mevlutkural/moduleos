import { Type } from 'class-transformer';
import { IsInt, IsNotEmpty, Max, Min } from 'class-validator';
import { ValidationMessages } from '../../constants/validation-messages';
import { Fields } from '../../constants/fields';

export class PaginationQueryDto {
  @IsNotEmpty({
    message: ValidationMessages.Required,
    context: { field: Fields.General.Page },
  })
  @IsInt({
    message: ValidationMessages.Int,
    context: { field: Fields.General.Page },
  })
  @Min(1, {
    message: ValidationMessages.Min,
    context: { field: Fields.General.Page, value: 1 },
  })
  @Type(() => Number)
  page: number;

  @IsNotEmpty({
    message: ValidationMessages.Required,
    context: { field: Fields.General.Limit },
  })
  @IsInt({
    message: ValidationMessages.Int,
    context: { field: Fields.General.Limit },
  })
  @Min(1, {
    message: ValidationMessages.Min,
    context: { field: Fields.General.Limit, value: 1 },
  })
  @Max(1000, {
    message: ValidationMessages.Max,
    context: { field: Fields.General.Limit, value: 1000 },
  })
  @Type(() => Number)
  limit: number;
}
