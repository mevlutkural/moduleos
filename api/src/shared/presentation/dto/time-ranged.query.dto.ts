import { IsDateString, IsOptional } from 'class-validator';
import { BaseQueryDto } from './base-query.dto';
import { ValidationMessages } from '../../constants/validation-messages';
import { Fields } from '../../constants/fields';

export class TimeRangedQueryDto extends BaseQueryDto {
  @IsOptional()
  @IsDateString(
    {},
    {
      message: ValidationMessages.Date,
      context: { field: Fields.General.StartDate },
    },
  )
  startDate?: Date;

  @IsOptional()
  @IsDateString(
    {},
    {
      message: ValidationMessages.Date,
      context: { field: Fields.General.EndDate },
    },
  )
  endDate?: Date;
}
