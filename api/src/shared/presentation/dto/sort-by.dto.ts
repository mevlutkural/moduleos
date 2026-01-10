import { IsIn, IsNotEmpty, IsString } from 'class-validator';
import { ValidationMessages } from '../../constants/validation-messages';
import { Fields } from '../../constants/fields';

export class SortByDto {
  @IsNotEmpty({
    message: ValidationMessages.Required,
    context: { field: Fields.General.Sort.Field },
  })
  @IsString({
    message: ValidationMessages.String,
    context: { field: Fields.General.Sort.Field },
  })
  field: string;

  @IsNotEmpty({
    message: ValidationMessages.Required,
    context: { field: Fields.General.Sort.Order },
  })
  @IsIn(['ASC', 'DESC'], {
    message: ValidationMessages.Enum,
    context: { field: Fields.General.Sort.Order, values: ['ASC', 'DESC'] },
  })
  order: 'ASC' | 'DESC';
}
