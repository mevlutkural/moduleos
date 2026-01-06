import { IsDateString, IsOptional } from 'class-validator';
import { BaseQueryDto } from './base-query.dto';
import { VALIDATION_MESSAGES } from '../constants/validation-messages';
import { FIELDS } from '../constants/fields';

export class TimeRangedQueryDto extends BaseQueryDto {
  @IsOptional()
  @IsDateString(
    {},
    {
      message: VALIDATION_MESSAGES.DATE,
      context: { field: FIELDS.GENERAL.START_DATE },
    },
  )
  startDate?: Date;

  @IsOptional()
  @IsDateString(
    {},
    {
      message: VALIDATION_MESSAGES.DATE,
      context: { field: FIELDS.GENERAL.END_DATE },
    },
  )
  endDate?: Date;
}
