import { IsIn, IsNotEmpty, IsString } from 'class-validator';
import { VALIDATION_MESSAGES } from '../../constants/validation-messages';
import { FIELDS } from '../../constants/fields';

export class SortByDto {
  @IsNotEmpty({
    message: VALIDATION_MESSAGES.REQUIRED,
    context: { field: FIELDS.GENERAL.SORT.FIELD },
  })
  @IsString({
    message: VALIDATION_MESSAGES.STRING,
    context: { field: FIELDS.GENERAL.SORT.FIELD },
  })
  field: string;

  @IsNotEmpty({
    message: VALIDATION_MESSAGES.REQUIRED,
    context: { field: FIELDS.GENERAL.SORT.ORDER },
  })
  @IsIn(['ASC', 'DESC'], {
    message: VALIDATION_MESSAGES.ENUM,
    context: { field: FIELDS.GENERAL.SORT.ORDER, values: ['ASC', 'DESC'] },
  })
  order: 'ASC' | 'DESC';
}
